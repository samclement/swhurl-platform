package kube

import (
	"encoding/base64"
	"encoding/json"
)

// Connection is what one kubeconfig names: the endpoint to reach and the material for reaching it. The
// context name is carried along as an identity for a report; it is not a credential.
type Connection struct {
	Endpoint    string
	Credentials Credentials
	Token       string
	Context     string
}

// kubeconfig is the JSON form of a kubeconfig, as `kubectl config view --raw -o json` prints it. v2 does
// not read the YAML file: understanding YAML is a dependency decision, and the operator's existing tool
// already prints this form. No part of it is copied, logged or kept.
type kubeconfig struct {
	Kind           string        `json:"kind"`
	CurrentContext string        `json:"current-context"`
	Clusters       []kubeCluster `json:"clusters"`
	Users          []kubeUser    `json:"users"`
	Contexts       []kubeContext `json:"contexts"`
}

// kubeCluster is one named endpoint and the authority that signs for it.
type kubeCluster struct {
	Name    string `json:"name"`
	Cluster struct {
		Server                   string `json:"server"`
		CertificateAuthorityData string `json:"certificate-authority-data"`
	} `json:"cluster"`
}

// kubeUser is one named user's credential material.
type kubeUser struct {
	Name string `json:"name"`
	User struct {
		ClientCertificateData string          `json:"client-certificate-data"`
		ClientKeyData         string          `json:"client-key-data"`
		Token                 string          `json:"token"`
		Exec                  json.RawMessage `json:"exec"`
		AuthProvider          json.RawMessage `json:"auth-provider"`
	} `json:"user"`
}

// kubeContext is one named context: which cluster a user reaches.
type kubeContext struct {
	Name    string `json:"name"`
	Context struct {
		Cluster string `json:"cluster"`
		User    string `json:"user"`
	} `json:"context"`
}

// Connect reads the JSON form of a kubeconfig and returns the connection its current context names. It
// refuses what it cannot use rather than sending a request it knows will fail: no current context, an
// unknown context, cluster or user, material that is not base64, or a credential plugin.
func Connect(data []byte) (Connection, error) {
	var document kubeconfig
	if err := json.Unmarshal(data, &document); err != nil {
		return Connection{}, configError("single_json_object_required")
	}
	current, found := document.current()
	if !found {
		return Connection{}, configError("current_context_required")
	}
	cluster, found := document.cluster(current.Context.Cluster)
	if !found {
		return Connection{}, configError("cluster_required")
	}
	user, found := document.user(current.Context.User)
	if !found {
		return Connection{}, configError("user_required")
	}
	connection := Connection{Endpoint: cluster.Cluster.Server, Context: current.Name}
	if connection.Endpoint == "" {
		return Connection{}, configError("server_required")
	}
	ca, err := decoded("ca", cluster.Cluster.CertificateAuthorityData)
	if err != nil {
		return Connection{}, err
	}
	credentials, token, err := credentialOf(user)
	if err != nil {
		return Connection{}, err
	}
	connection.Credentials = Credentials{CA: ca, Certificate: credentials.Certificate, Key: credentials.Key}
	connection.Token = token
	return connection, nil
}

// credentialOf decodes one user's material and refuses what this adapter cannot use: a plugin would run
// something else, and no credential at all would be an anonymous request the server refuses anyway.
func credentialOf(user kubeUser) (Credentials, string, error) {
	if len(user.User.Exec) > 0 || len(user.User.AuthProvider) > 0 {
		return Credentials{}, "", configError("unsupported_credential")
	}
	certificate, err := decoded("client_certificate", user.User.ClientCertificateData)
	if err != nil {
		return Credentials{}, "", err
	}
	key, err := decoded("client_key", user.User.ClientKeyData)
	if err != nil {
		return Credentials{}, "", err
	}
	if user.User.Token == "" && len(certificate) == 0 {
		return Credentials{}, "", configError("credential_required")
	}
	return Credentials{Certificate: certificate, Key: key}, user.User.Token, nil
}

// Reader builds the reader this connection describes.
func (c Connection) Reader() (Reader, error) {
	client, err := Client(c.Credentials)
	if err != nil {
		return nil, err
	}
	return New(c.Endpoint, client, c.Token)
}

// current is the context the kubeconfig names, if it names one that is present.
func (k kubeconfig) current() (kubeContext, bool) {
	for _, context := range k.Contexts {
		if context.Name == k.CurrentContext && k.CurrentContext != "" {
			return context, true
		}
	}
	return kubeContext{}, false
}

// cluster is the named cluster, which names the endpoint and its authority.
func (k kubeconfig) cluster(name string) (kubeCluster, bool) {
	for _, cluster := range k.Clusters {
		if cluster.Name == name {
			return cluster, true
		}
	}
	return kubeCluster{}, false
}

// user is the named user, which names the credential material.
func (k kubeconfig) user(name string) (kubeUser, bool) {
	for _, user := range k.Users {
		if user.Name == name {
			return user, true
		}
	}
	return kubeUser{}, false
}

// decoded is one base64 field of the kubeconfig, or nothing when it is absent.
func decoded(field, value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, configError("invalid_" + field)
	}
	return decoded, nil
}

func configError(reason string) *Error {
	return &Error{RefusedRequest, "kubeconfig", reason}
}
