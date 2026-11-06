// Package secretsprovider is the Secrets Store CSI Driver's provider "ezgh": it mounts EZGH Secrets
// fields as files, read with the pod's own pod identity key.
//
// v1alpha1/service.proto is the driver's provider API, unedited, from secrets-store-csi-driver
// v1.6.1 (copied rather than imported: the module would raise grpc and other shared modules).
// Regenerate with protoc 36.2, protoc-gen-go v1.36.12 and protoc-gen-go-grpc v1.6.2, from v1alpha1/:
//
//	protoc -I . --go_out=. --go_opt=paths=source_relative \
//	  '--go_opt=Mservice.proto=github.com/ezgamehost/ezgh-secrets-store-provider/internal/cloud/secretsprovider/v1alpha1;v1alpha1' \
//	  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
//	  '--go-grpc_opt=Mservice.proto=github.com/ezgamehost/ezgh-secrets-store-provider/internal/cloud/secretsprovider/v1alpha1;v1alpha1' \
//	  service.proto
package secretsprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"

	"github.com/ezgamehost/ezgh-secrets-store-provider/internal/cloud/secretsprovider/v1alpha1"
	"github.com/ezgamehost/ezgh-secrets-store-provider/internal/ezghclient"
	"github.com/ezgamehost/ezgh-secrets-store-provider/internal/ezghclient/apiclient"
	"github.com/ezgamehost/ezgh-secrets-store-provider/internal/ezghclient/secretsapi"
)

// Name is the provider's name in a SecretProviderClass (spec.provider) and its socket's.
const Name = "ezgh"

// Audience is the pod identity token's audience, which the driver's CSIDriver tokenRequests ask for.
const Audience = "pods.kubernetes.ezghcloud.com"

const tokensAttribute = "csi.storage.k8s.io/serviceAccount.tokens"

// Provider serves Mount. It keeps nothing between mounts: each one exchanges the pod's token at
// the node's pod identity agent and reads with that key, which then goes out of scope.
type Provider struct {
	v1alpha1.UnimplementedCSIDriverProviderServer
	// Org and Project are the cluster's; a SecretProviderClass's "project" parameter picks another.
	Org, Project string
	// CredentialsURL is the pod identity agent's http://169.254.170.23/v1/credentials.
	CredentialsURL string
	// ServiceURL is each EZGH API's base URL (ezghclient.ServiceURL when nil).
	ServiceURL func(host string) string
	HTTP       *http.Client
}

// object is one entry of the SecretProviderClass's "objects" parameter (YAML).
type object struct {
	Secret  string             `json:"secret"`            // sec_… or a name
	Version intstr.IntOrString `json:"version,omitempty"` // latest (default) or a number
	Field   string             `json:"field,omitempty"`   // one field; empty for every field
	Path    string             `json:"path,omitempty"`    // the field's file (default its name), or every field's directory
}

func (p *Provider) Version(context.Context, *v1alpha1.VersionRequest) (*v1alpha1.VersionResponse, error) {
	return &v1alpha1.VersionResponse{Version: "v1alpha1", RuntimeName: "ezgh-secrets-provider", RuntimeVersion: "0.1.0"}, nil
}

func (p *Provider) Mount(ctx context.Context, req *v1alpha1.MountRequest) (*v1alpha1.MountResponse, error) {
	var attrs map[string]string
	if err := json.Unmarshal([]byte(req.Attributes), &attrs); err != nil {
		return nil, status.Error(codes.InvalidArgument, "the mount's attributes aren't a JSON object of strings")
	}
	var mode int32 = 0o644
	if req.Permission != "" {
		m, err := strconv.ParseInt(req.Permission, 10, 32)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "permission %q isn't a file mode", req.Permission)
		}
		mode = int32(m)
	}
	var objects []object
	if err := yaml.UnmarshalStrict([]byte(attrs["objects"]), &objects); err != nil || len(objects) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, `parameters.objects must list [{secret, version?, field?, path?}]: %v`, err)
	}
	paths := map[string]bool{}
	for _, o := range objects {
		if o.Secret == "" {
			return nil, status.Error(codes.InvalidArgument, "every object names its secret")
		}
		if (o.Path != "" && !local(o.Path)) || strings.HasPrefix(o.Field, "..") {
			return nil, status.Errorf(codes.InvalidArgument, "path %q must be relative, stay inside the volume and have no part starting with ..", file(o, o.Field))
		}
		if o.Field != "" {
			if err := claim(paths, file(o, o.Field)); err != nil {
				return nil, err
			}
		}
	}
	token, err := podToken(attrs[tokensAttribute])
	if err != nil {
		return nil, err
	}
	project := p.Project
	if v := attrs["project"]; v != "" {
		project = v
	}

	key, err := p.exchange(ctx, token)
	if err != nil {
		return nil, err
	}
	url := p.ServiceURL
	if url == nil {
		url = ezghclient.ServiceURL
	}
	secrets := ezghclient.NewAt(podKey(key), "ezgh-secrets-provider", url).Secrets

	resp := &v1alpha1.MountResponse{}
	for _, o := range objects {
		id, err := p.secretID(ctx, secrets, project, o.Secret)
		if err != nil {
			return nil, err
		}
		version := o.Version.String()
		if version == "" || version == "0" {
			version = "latest"
		}
		if o.Field != "" {
			ans, err := secrets.ReadField(ctx, p.Org, project, id, version, o.Field)
			if err != nil {
				return nil, apiStatus(err)
			}
			b, err := ans.Value.Bytes()
			if err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
			resp.Files = append(resp.Files, &v1alpha1.File{Path: file(o, o.Field), Mode: mode, Contents: b})
			resp.ObjectVersion = append(resp.ObjectVersion, &v1alpha1.ObjectVersion{Id: id + "/" + o.Field, Version: strconv.Itoa(ans.Version)})
			continue
		}
		ans, err := secrets.ReadValue(ctx, p.Org, project, id, version)
		if err != nil {
			return nil, apiStatus(err)
		}
		names := make([]string, 0, len(ans.Fields))
		for name := range ans.Fields {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			path := file(o, name)
			if strings.HasPrefix(name, "..") || !local(path) {
				return nil, status.Errorf(codes.InvalidArgument, "secret %s has a field %q that can't be a file name; name the fields to mount", id, name)
			}
			if err := claim(paths, path); err != nil {
				return nil, err
			}
			b, err := ans.Fields[name].Bytes()
			if err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
			resp.Files = append(resp.Files, &v1alpha1.File{Path: path, Mode: mode, Contents: b})
		}
		resp.ObjectVersion = append(resp.ObjectVersion, &v1alpha1.ObjectVersion{Id: id, Version: strconv.Itoa(ans.Version)})
	}
	return resp, nil
}

// file is where field of o is written: o.Path for a named field, else under o.Path; cleaned, so
// ./a and a are one file.
func file(o object, field string) string {
	if o.Field != "" && o.Path != "" {
		return filepath.Clean(o.Path)
	}
	return filepath.Join(o.Path, field)
}

// local reports whether path stays inside the volume, with no part starting with "..", which the
// driver's atomic writer uses for its own (..data, ..<timestamp>).
func local(path string) bool {
	if !filepath.IsLocal(path) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if strings.HasPrefix(part, "..") {
			return false
		}
	}
	return true
}

func claim(paths map[string]bool, path string) error {
	if !local(path) {
		return status.Errorf(codes.InvalidArgument, "path %q must be relative, stay inside the volume and have no part starting with ..", path)
	}
	if paths[path] {
		return status.Errorf(codes.InvalidArgument, "two objects write %s", path)
	}
	paths[path] = true
	return nil
}

// podToken is the pod's pod identity token, from the tokens the driver's CSIDriver requested.
func podToken(tokens string) (string, error) {
	var t map[string]struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(tokens), &t)
	if t[Audience].Token == "" {
		return "", status.Errorf(codes.InvalidArgument, "the mount has no service account token for audience %s: the secrets-store CSIDriver's tokenRequests must ask for it", Audience)
	}
	return t[Audience].Token, nil
}

// exchange trades the pod's token for its API key at the node's pod identity agent, which answers
// kubernetes-api's status and error unchanged.
func (p *Provider) exchange(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.CredentialsURL, nil)
	if err != nil {
		return "", status.Error(codes.Internal, err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// A redirect is answered as-is: following it would send the pod's token wherever it points.
	client := *p.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", status.Errorf(codes.Unavailable, "the node's pod identity agent is unreachable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		APIKey string `json:"apiKey"`
		Error  struct{ Code, Message string }
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK {
		return "", status.Errorf(httpCode(resp.StatusCode), "pod identity refused the pod's token: %s (%s, %d)", body.Error.Message, body.Error.Code, resp.StatusCode)
	}
	if body.APIKey == "" {
		return "", status.Error(codes.Unavailable, "the pod identity agent answered without an apiKey")
	}
	return body.APIKey, nil
}

// secretID is ref's sec_ ID; a name is looked up and must match exactly.
func (p *Provider) secretID(ctx context.Context, c *secretsapi.Client, project, ref string) (string, error) {
	if strings.HasPrefix(ref, "sec_") {
		return ref, nil
	}
	page, err := c.ListSecrets(ctx, p.Org, project, ref, 10, "")
	if err != nil {
		return "", apiStatus(err)
	}
	for _, s := range page.Items {
		if s.Name == ref {
			return s.Slug, nil
		}
	}
	return "", status.Errorf(codes.NotFound, "no secret named %q in project %s", ref, project)
}

// podKey is a CredentialSource holding one mount's key.
type podKey string

func (k podKey) APIKey(context.Context) (string, error) { return string(k), nil }

// apiStatus is a Secrets answer as a gRPC status keeping the API's code and message.
func apiStatus(err error) error {
	if e, ok := apiclient.AsError(err); ok {
		return status.Errorf(httpCode(e.Status), "EZGH Secrets answered %d: %s (%s)", e.Status, e.Message, e.Code)
	}
	return status.Error(codes.Unavailable, err.Error())
}

func httpCode(s int) codes.Code {
	switch s {
	case http.StatusBadRequest:
		return codes.InvalidArgument
	case http.StatusUnauthorized:
		return codes.Unauthenticated
	case http.StatusForbidden:
		return codes.PermissionDenied
	case http.StatusNotFound:
		return codes.NotFound
	case http.StatusConflict, http.StatusGone:
		return codes.FailedPrecondition
	case http.StatusTooManyRequests:
		return codes.ResourceExhausted
	}
	return codes.Unavailable
}
