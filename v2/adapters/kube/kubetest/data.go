package kubetest

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// namePattern and keyPattern are the shapes the adapter accepts for a name and a ConfigMap key.
var (
	namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	keyPattern  = regexp.MustCompile(`^[-._a-zA-Z0-9]{1,253}$`)
)

// Review answers what Tokens says about the token, and that any other is not vouched for.
func (f *Fake) Review(_ context.Context, token, audience string) (kube.TokenReview, error) {
	f.Calls = append(f.Calls, "review")
	if token == "" || audience == "" {
		return kube.TokenReview{}, &kube.Error{Code: kube.RefusedRequest, Subject: "tokenreview", Reason: "token_and_audience_required"}
	}
	if err := f.failing("review"); err != nil {
		return kube.TokenReview{}, err
	}
	return f.Tokens[token], nil
}

// ConfigMaps lists the namespace's ConfigMaps the selector picks, as copies.
func (f *Fake) ConfigMaps(_ context.Context, namespace string, selector kube.Selector) ([]kube.ConfigMap, error) {
	if err := f.listing("configmaps", namespace, selector); err != nil {
		return nil, err
	}
	found := []kube.ConfigMap{}
	for _, configMap := range f.ConfigMapData[namespace] {
		if selector.Name == configMap.Name || selector.Labels != "" && matches(selector.Labels, configMap.Labels) {
			found = append(found, kube.ConfigMap{Name: configMap.Name, Labels: maps.Clone(configMap.Labels),
				Data: maps.Clone(configMap.Data)})
		}
	}
	return found, nil
}

// Secrets lists the namespace's Secrets the selector picks, as copies.
func (f *Fake) Secrets(_ context.Context, namespace string, selector kube.Selector) ([]kube.Secret, error) {
	if err := f.listing("secrets", namespace, selector); err != nil {
		return nil, err
	}
	found := []kube.Secret{}
	for _, secret := range f.SecretData[namespace] {
		if selector.Name == secret.Name || selector.Labels != "" && matches(selector.Labels, secret.Labels) {
			data := map[string][]byte{}
			for key, value := range secret.Data {
				data[key] = bytes.Clone(value)
			}
			found = append(found, kube.Secret{Name: secret.Name, Labels: maps.Clone(secret.Labels), Data: data})
		}
	}
	return found, nil
}

// SecretNames lists the names of the namespace's Secrets the labels pick, as the metadata-only list does; a failure
// the scenario names as "secretnames <labels>" is returned after the call is recorded.
func (f *Fake) SecretNames(_ context.Context, namespace, labels string) ([]string, error) {
	if labels == "" {
		return nil, &kube.Error{Code: kube.RefusedRequest, Subject: "selector", Reason: "required"}
	}
	if err := f.listing("secretnames", namespace, kube.Selector{Labels: labels}); err != nil {
		return nil, err
	}
	names := []string{}
	for _, secret := range f.SecretData[namespace] {
		if matches(labels, secret.Labels) {
			names = append(names, secret.Name)
		}
	}
	return names, nil
}

// GetSecret is the fake's named get of a Secret, distinct from the selected list and its permission: a copy, or nil
// when the fake holds none of that name.
func (f *Fake) GetSecret(_ context.Context, namespace, name string) (*kube.Secret, error) {
	f.Calls = append(f.Calls, "get secret "+name)
	if !namePattern.MatchString(namespace) || !namePattern.MatchString(name) {
		return nil, &kube.Error{Code: kube.RefusedRequest, Subject: "secret", Reason: "invalid_name"}
	}
	if err := f.failing("get secret " + name); err != nil {
		return nil, err
	}
	for _, secret := range f.SecretData[namespace] {
		if secret.Name == name {
			data := map[string][]byte{}
			for key, value := range secret.Data {
				data[key] = bytes.Clone(value)
			}
			return &kube.Secret{Name: secret.Name, Labels: maps.Clone(secret.Labels), Data: data}, nil
		}
	}
	return nil, nil
}

// SetConfigMapKey sets the key of a ConfigMap the fake holds; one it does not hold is not found, as a patch of it is.
func (f *Fake) SetConfigMapKey(ctx context.Context, namespace, name, key, value, fieldManager string) error {
	return f.SetConfigMapKeys(ctx, namespace, name, map[string]*string{key: &value}, fieldManager)
}

// SetConfigMapKeys sets and deletes the keys of a ConfigMap the fake holds, as the adapter's one merge patch does.
func (f *Fake) SetConfigMapKeys(_ context.Context, namespace, name string, keys map[string]*string, fieldManager string) error {
	f.Calls = append(f.Calls, "set "+name)
	if err := setKeysProblem(namespace, name, keys, fieldManager); err != nil {
		return err
	}
	if err := f.failing("set " + name); err != nil {
		return err
	}
	return f.setKeys(namespace, name, keys)
}

// setKeysProblem is what a key set refuses before any request, as the adapter does.
func setKeysProblem(namespace, name string, keys map[string]*string, fieldManager string) error {
	if !namePattern.MatchString(namespace) || !namePattern.MatchString(name) {
		return &kube.Error{Code: kube.RefusedRequest, Subject: "configmap", Reason: "invalid_name"}
	}
	for key := range keys {
		if !keyPattern.MatchString(key) {
			return &kube.Error{Code: kube.RefusedRequest, Subject: "configmap", Reason: "invalid_name"}
		}
	}
	if fieldManager == "" || len(fieldManager) > 128 {
		return &kube.Error{Code: kube.RefusedRequest, Subject: "configmap", Reason: "field_manager_required"}
	}
	return nil
}

// setKeys applies one key set to the ConfigMap the fake holds; one it does not hold is not found.
func (f *Fake) setKeys(namespace, name string, keys map[string]*string) error {
	for i, configMap := range f.ConfigMapData[namespace] {
		if configMap.Name != name {
			continue
		}
		data := maps.Clone(configMap.Data)
		if data == nil {
			data = map[string]string{}
		}
		for key, value := range keys {
			if value == nil {
				delete(data, key)
				continue
			}
			data[key] = *value
		}
		f.ConfigMapData[namespace][i].Data = data
		return nil
	}
	return &kube.Error{Code: kube.Failed, Subject: "configmap", Reason: observe.ReasonNotFound}
}

// listing records one list and refuses it as the adapter would: a selector that picks nothing or twice, an invalid
// namespace, or a failure the scenario names.
func (f *Fake) listing(collection, namespace string, selector kube.Selector) error {
	operation := collection + " " + selector.Name + selector.Labels
	f.Calls = append(f.Calls, operation)
	switch {
	case (selector.Name == "") == (selector.Labels == ""):
		return &kube.Error{Code: kube.RefusedRequest, Subject: "selector", Reason: "one_of_name_or_labels_required"}
	case selector.Name != "" && !namePattern.MatchString(selector.Name):
		return &kube.Error{Code: kube.RefusedRequest, Subject: "selector", Reason: "invalid_name"}
	case !namePattern.MatchString(namespace):
		return &kube.Error{Code: kube.RefusedRequest, Subject: collection, Reason: "invalid_namespace"}
	}
	return f.failing(operation)
}

func (f *Fake) failing(operation string) error {
	if f.Refuse != nil {
		return f.Refuse
	}
	return f.Failing[operation]
}

// matches is whether labels satisfy an equality selector, key=value[,key=value].
func matches(selector string, labels map[string]string) bool {
	for _, term := range strings.Split(selector, ",") {
		key, value, ok := strings.Cut(term, "=")
		if !ok || labels[key] != value {
			return false
		}
	}
	return true
}

// ReviewAnswers is the scenario the review contract runs: a token the API server vouches for, for an audience,
// as the user it names.
type ReviewAnswers struct{ Token, Audience, Username string }

// ReviewContract is the behaviour every kube.Reviewer must have: a vouched-for token is the user it names, any other
// token is an answer that vouches for nothing rather than an error, and a review without a token or an audience is
// refused before anything is asked.
func ReviewContract(t *testing.T, answers ReviewAnswers, open func(*testing.T, ReviewAnswers) kube.Reviewer) {
	t.Helper()
	reviewer := open(t, answers)
	review, err := reviewer.Review(context.Background(), answers.Token, answers.Audience)
	if err != nil || !review.Authenticated || review.Username != answers.Username ||
		!reflect.DeepEqual(review.Audiences, []string{answers.Audience}) {
		t.Fatalf("a vouched-for token: %+v, %v", review, err)
	}
	review, err = reviewer.Review(context.Background(), "forged-token", answers.Audience)
	if err != nil || review.Authenticated || review.Username != "" {
		t.Fatalf("a forged token: %+v, %v", review, err)
	}
	for _, request := range [][2]string{{"", answers.Audience}, {answers.Token, ""}} {
		if _, err := reviewer.Review(context.Background(), request[0], request[1]); !refused(err, "token_and_audience_required") {
			t.Fatalf("a review without a token or an audience: %v", err)
		}
	}
}

// DataAnswers is the scenario the data contract runs: one namespace holding a ConfigMap, a Secret its labels select
// and, under Other, a Secret they do not.
type DataAnswers struct {
	Namespace, Labels string
	ConfigMap         kube.ConfigMap
	Secret            kube.Secret
	Other             string
}

// DataContract is the behaviour every kube.Data must have: a list by name or by labels is exactly the objects it
// picks, an empty list is not a failure, the caller owns what it is given, a key can be set, and a request the
// adapter cannot make is refused.
func DataContract(t *testing.T, answers DataAnswers, open func(*testing.T, DataAnswers) kube.Data) {
	t.Helper()
	data := open(t, answers)
	ctx := context.Background()
	configMaps, err := data.ConfigMaps(ctx, answers.Namespace, kube.Selector{Name: answers.ConfigMap.Name})
	if err != nil || !reflect.DeepEqual(configMaps, []kube.ConfigMap{answers.ConfigMap}) {
		t.Fatalf("a ConfigMap by name: %+v, %v", configMaps, err)
	}
	for _, configMap := range configMaps {
		clear(configMap.Data)
	}
	again, err := data.ConfigMaps(ctx, answers.Namespace, kube.Selector{Name: answers.ConfigMap.Name})
	if err != nil || !reflect.DeepEqual(again, []kube.ConfigMap{answers.ConfigMap}) {
		t.Fatalf("a caller changed a later answer: %+v, %v", again, err)
	}
	if absent, err := data.ConfigMaps(ctx, answers.Namespace, kube.Selector{Name: "absent"}); err != nil || len(absent) != 0 {
		t.Fatalf("an absent ConfigMap: %+v, %v", absent, err)
	}
	secretsArePicked(t, data, answers)
	secretIsGot(t, data, answers)
	keyIsSet(t, data, answers)
	dataRefusals(t, data, answers)
}

// keyIsSet: a key that is set is read back beside the keys the ConfigMap already held, and a ConfigMap that is not
// there is a failure, never created.
func keyIsSet(t *testing.T, data kube.Data, answers DataAnswers) {
	t.Helper()
	ctx := context.Background()
	if err := data.SetConfigMapKey(ctx, answers.Namespace, answers.ConfigMap.Name, "state.json", `{"version":1}`, "notify"); err != nil {
		t.Fatalf("setting a key: %v", err)
	}
	want := maps.Clone(answers.ConfigMap.Data)
	if want == nil {
		want = map[string]string{}
	}
	want["state.json"] = `{"version":1}`
	after, err := data.ConfigMaps(ctx, answers.Namespace, kube.Selector{Name: answers.ConfigMap.Name})
	if err != nil || len(after) != 1 || !reflect.DeepEqual(after[0].Data, want) {
		t.Fatalf("the key set: %+v, %v", after, err)
	}
	err = data.SetConfigMapKey(ctx, answers.Namespace, "absent", "state.json", "{}", "notify")
	var failure *kube.Error
	if !errors.As(err, &failure) || failure.Code != kube.Failed || failure.Reason != observe.ReasonNotFound {
		t.Fatalf("setting a key of an absent ConfigMap: %v", err)
	}
}

// secretsArePicked: by labels the selected Secret only, by name the named one, each with its data decoded.
func secretsArePicked(t *testing.T, data kube.Data, answers DataAnswers) {
	t.Helper()
	for _, selector := range []kube.Selector{{Labels: answers.Labels}, {Name: answers.Secret.Name}} {
		secrets, err := data.Secrets(context.Background(), answers.Namespace, selector)
		if err != nil || !reflect.DeepEqual(secrets, []kube.Secret{answers.Secret}) {
			t.Fatalf("secrets by %+v: %d, %v", selector, len(secrets), err)
		}
	}
	other, err := data.Secrets(context.Background(), answers.Namespace, kube.Selector{Name: answers.Other})
	if err != nil || len(other) != 1 || other[0].Name != answers.Other {
		t.Fatalf("the other Secret by name: %d, %v", len(other), err)
	}
}

// secretIsGot: a Secret by name is the one object with its data decoded, the caller owns it, and a Secret that is
// not there is absence (nil), not an error.
func secretIsGot(t *testing.T, data kube.Data, answers DataAnswers) {
	t.Helper()
	ctx := context.Background()
	secret, err := data.GetSecret(ctx, answers.Namespace, answers.Secret.Name)
	if err != nil || secret == nil || !reflect.DeepEqual(*secret, answers.Secret) {
		t.Fatalf("a Secret by name: %v", err)
	}
	for key := range secret.Data {
		secret.Data[key][0] ^= 0xff
	}
	again, err := data.GetSecret(ctx, answers.Namespace, answers.Secret.Name)
	if err != nil || again == nil || !reflect.DeepEqual(*again, answers.Secret) {
		t.Fatalf("a caller changed a later answer: %v", err)
	}
	secretAbsentOrRefused(t, data, answers)
}

// secretAbsentOrRefused: a Secret that is not there is nil, and a name that cannot be one is refused.
func secretAbsentOrRefused(t *testing.T, data kube.Data, answers DataAnswers) {
	t.Helper()
	ctx := context.Background()
	if absent, err := data.GetSecret(ctx, answers.Namespace, "absent"); err != nil || absent != nil {
		t.Fatalf("an absent Secret: %v", err)
	}
	if _, err := data.GetSecret(ctx, answers.Namespace, "Not_A_Name"); !refused(err, "invalid_name") {
		t.Fatalf("an invalid name: %v", err)
	}
}

// dataRefusals: a selector that picks nothing or picks twice, an invalid namespace, a key that is not one and a
// change without a field manager are refusals, never an empty answer.
func dataRefusals(t *testing.T, data kube.Data, answers DataAnswers) {
	t.Helper()
	ctx := context.Background()
	for _, selector := range []kube.Selector{{}, {Name: "a", Labels: "b=c"}} {
		if _, err := data.ConfigMaps(ctx, answers.Namespace, selector); !refused(err, "one_of_name_or_labels_required") {
			t.Fatalf("selector %+v: %v", selector, err)
		}
	}
	if _, err := data.Secrets(ctx, "Not_A_Namespace", kube.Selector{Name: "a"}); !refused(err, "invalid_namespace") {
		t.Fatalf("an invalid namespace: %v", err)
	}
	if err := data.SetConfigMapKey(ctx, answers.Namespace, answers.ConfigMap.Name, "a/b", "", "notify"); !refused(err, "invalid_name") {
		t.Fatalf("an invalid key: %v", err)
	}
	err := data.SetConfigMapKey(ctx, answers.Namespace, answers.ConfigMap.Name, "state.json", "", "")
	var refusal *kube.Error
	if !errors.As(err, &refusal) || refusal.Reason != "field_manager_required" {
		t.Fatalf("no field manager: %v", err)
	}
}
