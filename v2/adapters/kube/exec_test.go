package kube_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
)

func execRequest(t *testing.T) kube.ExecRequest {
	t.Helper()
	var r kube.ExecRequest
	if err := json.Unmarshal(document(t, "exec.json"), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// wsServer exercises net/http's real TLS upgrade, buffering and bidirectional response body.
func wsServer(t *testing.T, serve func(net.Conn, *http.Request)) kube.Executor {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("authorization not reused")
		}
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		fmt.Fprintf(buffer, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\nSec-WebSocket-Protocol: v5.channel.k8s.io\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
		buffer.Flush()
		serve(conn, r)
	}))
	t.Cleanup(server.Close)
	api, err := kube.NewAPI(server.URL, server.Client(), "synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func serverFrame(conn net.Conn, opcode byte, final bool, body []byte) {
	first := opcode
	if final {
		first |= 128
	}
	header := []byte{first, byte(len(body))}
	if len(body) >= 65536 {
		header = []byte{first, 127}
		header = binary.BigEndian.AppendUint64(header, uint64(len(body)))
	} else if len(body) >= 126 {
		header = []byte{first, 126}
		header = binary.BigEndian.AppendUint16(header, uint16(len(body)))
	}
	conn.Write(append(header, body...))
}

// clientFrame validates masking and returns the mask too, so fresh randomness is observable.
func clientFrame(t *testing.T, conn net.Conn) (byte, []byte, [4]byte) {
	t.Helper()
	var head [2]byte
	var mask [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		t.Error(err)
		return 0, nil, mask
	}
	if head[0]&128 == 0 || head[1]&128 == 0 {
		t.Error("client frame not final/masked")
	}
	size := int(head[1] & 127)
	if size == 126 {
		var n [2]byte
		io.ReadFull(conn, n[:])
		size = int(binary.BigEndian.Uint16(n[:]))
	}
	if size == 127 {
		t.Error("unexpectedly large client frame")
		return 0, nil, mask
	}
	io.ReadFull(conn, mask[:])
	body := make([]byte, size)
	io.ReadFull(conn, body)
	for i := range body {
		body[i] ^= mask[i%4]
	}
	return head[0] & 15, body, mask
}

func receiveStdin(t *testing.T, conn net.Conn, want []byte) {
	t.Helper()
	var stdin []byte
	masks := map[[4]byte]bool{}
	for {
		opcode, body, mask := clientFrame(t, conn)
		if masks[mask] {
			t.Error("reused mask")
		}
		masks[mask] = true
		if opcode != 2 || len(body) == 0 {
			t.Error("not a binary stdin message")
			return
		}
		if bytes.Equal(body, []byte{255, 0}) {
			break
		}
		if body[0] != 0 {
			t.Error("wrong stdin channel")
			return
		}
		stdin = append(stdin, body[1:]...)
	}
	if !bytes.Equal(stdin, want) {
		t.Error("stdin differs")
	}
}

func TestExecutorContract(t *testing.T) {
	request := execRequest(t)
	kubetest.ExecutorContract(t, request, func(t *testing.T, result kube.ExecResult, failed bool) kube.Executor {
		return wsServer(t, func(conn net.Conn, r *http.Request) {
			if r.Method != "GET" || r.URL.Path != "/api/v1/namespaces/observability/pods/clickstack-mongodb-0/exec" ||
				r.URL.Query().Get("container") != "mongod" || !reflect.DeepEqual(r.URL.Query()["command"], request.Command) || r.URL.Query().Get("stdin") != "true" || r.Header.Get("Sec-WebSocket-Protocol") != "v5.channel.k8s.io" {
				t.Error("wrong exec destination/query")
			}
			if strings.Contains(r.URL.String(), string(request.Stdin)) {
				t.Error("stdin in URL")
			}
			receiveStdin(t, conn, request.Stdin)
			serverFrame(conn, 2, true, append([]byte{1}, result.Stdout...))
			if failed {
				return
			}
			status := []byte(`{"status":"Success"}`)
			if result.ExitCode != 0 {
				status = []byte(fmt.Sprintf(`{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"%d"}]}}`, result.ExitCode))
			}
			// Kubernetes StatusCause uses reason, not type, in its JSON representation.
			serverFrame(conn, 2, true, append([]byte{3}, status...))
		})
	})
}

func TestFakeExecutorContractAndOwnership(t *testing.T) {
	request := execRequest(t)
	kubetest.ExecutorContract(t, request, func(t *testing.T, r kube.ExecResult, failed bool) kube.Executor {
		answer := kubetest.ExecAnswer{Result: r}
		if failed {
			answer.Err = fmt.Errorf("synthetic failure")
		}
		return kubetest.NewExecutor(answer, answer)
	})
	source := []byte("owned")
	fake := kubetest.NewExecutor(kubetest.ExecAnswer{Result: kube.ExecResult{Stdout: source}})
	source[0] = 'X'
	result, _ := fake.Exec(context.Background(), request)
	request.Stdin[0] ^= 0xff
	request.Command[0] = "changed"
	if string(result.Stdout) != "owned" || fake.Requests[0].Command[0] == "changed" || bytes.Equal(fake.Requests[0].Stdin, request.Stdin) {
		t.Fatal("mutable data aliased")
	}
}

func TestExecFragmentControlStatusAndTrailingOutput(t *testing.T) {
	executor := wsServer(t, func(conn net.Conn, _ *http.Request) {
		serverFrame(conn, 2, false, []byte{1, 'a'})
		serverFrame(conn, 9, true, []byte("ping"))
		opcode, pong, _ := clientFrame(t, conn)
		if opcode != 10 || string(pong) != "ping" {
			t.Error("no pong")
		}
		serverFrame(conn, 0, true, []byte("b"))
		serverFrame(conn, 10, true, nil)
		serverFrame(conn, 2, true, []byte("\x03{\"status\":\"Success\"}"))
		serverFrame(conn, 2, true, []byte("\x01c"))
		serverFrame(conn, 2, true, []byte{255, 1})
		serverFrame(conn, 2, true, []byte{255, 2})
		serverFrame(conn, 2, true, []byte{255, 3})
		serverFrame(conn, 8, true, []byte{3, 232})
		opcode, _, _ = clientFrame(t, conn)
		if opcode != 8 {
			t.Error("no close reply")
		}
	})
	request := execRequest(t)
	request.Stdin = nil
	result, err := executor.Exec(context.Background(), request)
	if err != nil || string(result.Stdout) != "abc" {
		t.Fatalf("%v, %v", result, err)
	}
}

func TestExecProtocolFailuresAreSafe(t *testing.T) {
	secret := "synthetic-key-and-mongodb://user:password@host"
	cases := map[string]func(net.Conn){
		"no status":  func(c net.Conn) { serverFrame(c, 2, true, []byte("\x01"+secret)) },
		"bad status": func(c net.Conn) { serverFrame(c, 2, true, []byte("\x03"+secret)) },
		"missing exit": func(c net.Conn) {
			serverFrame(c, 2, true, []byte(``+"\x03"+`{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[]}}`))
		},
		"bad exit": func(c net.Conn) {
			serverFrame(c, 2, true, []byte("\x03"+`{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"oops"}]}}`))
		},
		"masked server": func(c net.Conn) { c.Write([]byte{130, 128}) },
		"rsv":           func(c net.Conn) { c.Write([]byte{194, 0}) },
		"huge length":   func(c net.Conn) { c.Write([]byte{130, 127, 0, 0, 0, 0, 1, 0, 0, 0}) },
		"noncanonical":  func(c net.Conn) { c.Write([]byte{130, 126, 0, 1, 1}) },
		"truncated":     func(c net.Conn) { c.Write([]byte{130, 5, 1, 'a'}) },
		"truncated after status": func(c net.Conn) {
			serverFrame(c, 2, true, []byte("\x03{\"status\":\"Success\"}"))
			c.Write([]byte{130})
		},
		"fragment missing":     func(c net.Conn) { serverFrame(c, 0, true, []byte("\x01a")) },
		"fragment interrupted": func(c net.Conn) { serverFrame(c, 2, false, []byte{1}); serverFrame(c, 2, true, []byte{1}) },
		"text":                 func(c net.Conn) { serverFrame(c, 1, true, []byte(secret)) },
		"control fragmented":   func(c net.Conn) { serverFrame(c, 9, false, nil) },
		"control huge":         func(c net.Conn) { serverFrame(c, 9, true, make([]byte, 126)) },
		"close malformed":      func(c net.Conn) { serverFrame(c, 8, true, []byte{0}) },
		"close before status":  func(c net.Conn) { serverFrame(c, 8, true, nil) },
		"unknown channel":      func(c net.Conn) { serverFrame(c, 2, true, []byte{4, 1}) },
		"aggregate limit": func(c net.Conn) {
			for i := 0; i < 17; i++ {
				serverFrame(c, 2, true, append([]byte{2}, bytes.Repeat([]byte{'x'}, 65536)...))
			}
		},
		"closed channel": func(c net.Conn) {
			serverFrame(c, 2, true, []byte{255, 1})
			serverFrame(c, 2, true, []byte("\x01"+secret))
		},
	}
	for name, serve := range cases {
		t.Run(name, func(t *testing.T) {
			executor := wsServer(t, func(c net.Conn, _ *http.Request) { serve(c) })
			request := execRequest(t)
			request.Stdin = nil
			result, err := executor.Exec(context.Background(), request)
			if err == nil || len(result.Stdout) != 0 {
				t.Fatal("malformed/incomplete stream passed")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("secret in error")
			}
		})
	}
}

// A terminal status does not make an error Close orderly: 1000/1001 and no code end the run, others fail it.
func TestExecErrorCloseAfterStatusFails(t *testing.T) {
	for _, code := range []uint16{1002, 1003, 1008, 1011, 1012} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			executor := wsServer(t, func(c net.Conn, _ *http.Request) {
				serverFrame(c, 2, true, []byte("\x01answer"))
				serverFrame(c, 2, true, []byte("\x03{\"status\":\"Success\"}"))
				serverFrame(c, 8, true, []byte{byte(code >> 8), byte(code)})
			})
			request := execRequest(t)
			request.Stdin = nil
			if result, err := executor.Exec(context.Background(), request); err == nil || len(result.Stdout) > 0 {
				t.Fatalf("close %d passed: %+v %v", code, result, err)
			}
		})
	}
	for _, body := range [][]byte{{3, 232}, {3, 233}, nil} {
		name := fmt.Sprint(body)
		t.Run("orderly "+name, func(t *testing.T) {
			executor := wsServer(t, func(c net.Conn, _ *http.Request) {
				serverFrame(c, 2, true, []byte("\x01answer"))
				serverFrame(c, 2, true, []byte("\x03{\"status\":\"Success\"}"))
				serverFrame(c, 8, true, body)
			})
			request := execRequest(t)
			request.Stdin = nil
			if result, err := executor.Exec(context.Background(), request); err != nil || string(result.Stdout) != "answer" {
				t.Fatalf("orderly close %s: %+v %v", name, result, err)
			}
		})
	}
}

func TestExecUpgradeRefusalsAndCancellation(t *testing.T) {
	for _, kind := range []string{"denied", "accept", "protocol", "upgrade", "connection", "extension"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "denied" {
					w.WriteHeader(403)
					w.Write([]byte("synthetic-secret"))
					return
				}
				sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
				w.Header().Set("Connection", "Upgrade")
				w.Header().Set("Upgrade", "websocket")
				w.Header().Set("Sec-WebSocket-Accept", base64.StdEncoding.EncodeToString(sum[:]))
				w.Header().Set("Sec-WebSocket-Protocol", "v5.channel.k8s.io")
				if kind == "accept" {
					w.Header().Set("Sec-WebSocket-Accept", "wrong")
				} else if kind == "extension" { // a valid handshake selecting an extension this client never offered
					w.Header().Set("Sec-WebSocket-Extensions", "permessage-deflate")
				} else {
					w.Header().Set(map[string]string{"protocol": "Sec-WebSocket-Protocol", "upgrade": "Upgrade", "connection": "Connection"}[kind], "wrong")
				}
				w.WriteHeader(101)
			}))
			defer server.Close()
			api, _ := kube.NewAPI(server.URL, server.Client(), "")
			_, err := api.Exec(context.Background(), execRequest(t))
			if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("unsafe upgrade failure")
			}
		})
	}
	var wg sync.WaitGroup
	executor := wsServer(t, func(c net.Conn, _ *http.Request) { wg.Add(1); defer wg.Done(); io.Copy(io.Discard, c) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := executor.Exec(ctx, execRequest(t))
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("cancellation did not unblock upgraded connection")
	}
	wg.Wait()
}

func TestExecRejectsMalformedTerminalStatus(t *testing.T) {
	for _, status := range []string{
		`null`, `{}`, `{"status":"Unknown"}`, `{"status":"Failure"}`,
		`{"status":"Success","reason":"NonZeroExitCode"}`,
		`{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"-1"}]}}`,
		`{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"256"}]}}`,
		`{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"0"}]}}`,
		`{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"1"},{"reason":"ExitCode","message":"2"}]}}`,
	} {
		t.Run(status, func(t *testing.T) {
			executor := wsServer(t, func(c net.Conn, _ *http.Request) { serverFrame(c, 2, true, append([]byte{3}, []byte(status)...)) })
			request := execRequest(t)
			request.Stdin = nil
			if result, err := executor.Exec(context.Background(), request); err == nil || len(result.Stdout) > 0 {
				t.Fatal("invalid status passed")
			}
		})
	}
}

func TestExecDiscardsStderrAndRejectsUnfinishedFragment(t *testing.T) {
	for _, fragment := range []bool{false, true} {
		t.Run(fmt.Sprint(fragment), func(t *testing.T) {
			executor := wsServer(t, func(c net.Conn, _ *http.Request) {
				serverFrame(c, 2, true, []byte("\x02mongodb://synthetic-secret@db"))
				serverFrame(c, 2, true, []byte("\x03{\"status\":\"Success\"}"))
				serverFrame(c, 2, !fragment, []byte("\x01answer"))
			})
			request := execRequest(t)
			request.Stdin = nil
			result, err := executor.Exec(context.Background(), request)
			if fragment {
				if err == nil {
					t.Fatal("truncated fragmented output passed")
				}
				return
			}
			if err != nil || string(result.Stdout) != "answer" {
				t.Fatal("stderr retained or stdout lost")
			}
		})
	}
}

func TestExecCancellationWhileWaitingForUpgrade(t *testing.T) {
	done := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done(); close(done) }))
	defer server.Close()
	api, _ := kube.NewAPI(server.URL, server.Client(), "")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := api.Exec(ctx, execRequest(t))
	if err == nil {
		t.Fatal("withheld upgrade passed")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("withheld upgrade not cancelled")
	}
}
