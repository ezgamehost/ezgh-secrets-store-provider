package secretsprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ezgamehost/ezgh-secrets-store-provider/internal/cloud/secretsprovider/v1alpha1"
)

// fake is the node's pod identity agent and secrets-api, as a pod's minted key sees them.
type fake struct {
	refuse    bool // the agent's exchange answers 403
	exchanges atomic.Int32
	reads     atomic.Int32
}

func (f *fake) provider(t *testing.T) *Provider {
	t.Helper()
	write := func(w http.ResponseWriter, status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.exchanges.Add(1)
		if r.URL.Path != "/v1/credentials" || r.Header.Get("Authorization") != "Bearer pod-token" {
			write(w, 400, map[string]any{"error": map[string]string{"code": "invalid_request", "message": "no token"}})
			return
		}
		if f.refuse {
			write(w, 403, map[string]any{"error": map[string]string{"code": "access_denied", "message": "no pod identity association for default/web"}})
			return
		}
		write(w, 200, map[string]any{"apiKey": "ezgh_pod", "expiresAt": "2026-11-06T13:00:00.000Z", "bot": "bot_a"})
	}))
	t.Cleanup(agent.Close)
	base := "/v1/organizations/org_a/projects/prj_b/secrets"
	mux := http.NewServeMux()
	key := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer ezgh_pod" {
			write(w, 401, map[string]any{"error": map[string]string{"code": "unauthenticated", "message": "no key"}})
			return false
		}
		f.reads.Add(1)
		return true
	}
	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		if !key(w, r) {
			return
		}
		secrets := []map[string]any{}
		if r.URL.Query().Get("name") == "db" {
			secrets = append(secrets, map[string]any{"id": "0199c7a2-3333-7000-8000-000000000003", "slug": "sec_db", "name": "db"})
		}
		write(w, 200, map[string]any{"secrets": secrets, "nextCursor": nil})
	})
	mux.HandleFunc("GET "+base+"/{secret}/versions/{version}/value", func(w http.ResponseWriter, r *http.Request) {
		if !key(w, r) {
			return
		}
		if r.PathValue("secret") == "sec_gone" {
			write(w, 404, map[string]any{"error": map[string]string{"code": "not_found", "message": "no such secret"}})
			return
		}
		fields := map[string]any{"username": "app", "password": "s3cret", "cert": map[string]string{"base64": "AAEC"}}
		switch r.PathValue("secret") {
		case "sec_evil":
			fields = map[string]any{"../escape": "x"}
		case "sec_dots":
			fields = map[string]any{"..data": "x"}
		}
		write(w, 200, map[string]any{"version": 3, "fields": fields})
	})
	mux.HandleFunc("GET "+base+"/{secret}/versions/{version}/fields/{field}", func(w http.ResponseWriter, r *http.Request) {
		if !key(w, r) {
			return
		}
		v := 7
		if r.PathValue("version") == "2" {
			v = 2
		}
		write(w, 200, map[string]any{"version": v, "name": r.PathValue("field"), "value": "field-of-" + r.PathValue("secret")})
	})
	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)
	return &Provider{Org: "org_a", Project: "prj_b", CredentialsURL: agent.URL + "/v1/credentials", ServiceURL: func(string) string { return api.URL }, HTTP: http.DefaultClient}
}

func mountRequest(objects string, extra map[string]string) *v1alpha1.MountRequest {
	attrs := map[string]string{
		"objects":                                  objects,
		"csi.storage.k8s.io/pod.namespace":         "default",
		"csi.storage.k8s.io/serviceAccount.name":   "web",
		"csi.storage.k8s.io/serviceAccount.tokens": `{"pods.kubernetes.ezghcloud.com":{"token":"pod-token","expirationTimestamp":"2026-11-07T12:00:00Z"}}`,
	}
	for k, v := range extra {
		attrs[k] = v
	}
	b, _ := json.Marshal(attrs)
	return &v1alpha1.MountRequest{Attributes: string(b), Secrets: "{}", TargetPath: "/var/lib/kubelet/pods/x/volumes/y", Permission: "420"}
}

func files(r *v1alpha1.MountResponse) map[string]string {
	out := map[string]string{}
	for _, f := range r.Files {
		out[f.Path] = string(f.Contents)
	}
	return out
}

func versions(r *v1alpha1.MountResponse) []string {
	var out []string
	for _, v := range r.ObjectVersion {
		out = append(out, v.Id+"@"+v.Version)
	}
	sort.Strings(out)
	return out
}

var ctx = context.Background()

func TestMountAllFields(t *testing.T) {
	f := &fake{}
	resp, err := f.provider(t).Mount(ctx, mountRequest("- secret: sec_app\n", nil))
	if err != nil {
		t.Fatal(err)
	}
	got := files(resp)
	if len(got) != 3 || got["username"] != "app" || got["password"] != "s3cret" {
		t.Fatal(got)
	}
	for _, file := range resp.Files {
		if file.Mode != 0o644 {
			t.Fatalf("%s mode %o", file.Path, file.Mode)
		}
	}
	if v := versions(resp); len(v) != 1 || v[0] != "sec_app@3" {
		t.Fatal(v)
	}
}

func TestMountOneFieldWithPath(t *testing.T) {
	f := &fake{}
	resp, err := f.provider(t).Mount(ctx, mountRequest("- {secret: sec_app, field: password, path: db/password.txt, version: 2}\n- {secret: sec_other, field: token}\n", nil))
	if err != nil {
		t.Fatal(err)
	}
	got := files(resp)
	if len(got) != 2 || got["db/password.txt"] != "field-of-sec_app" || got["token"] != "field-of-sec_other" {
		t.Fatal(got)
	}
	if v := versions(resp); strings.Join(v, ",") != "sec_app/password@2,sec_other/token@7" {
		t.Fatal(v)
	}
}

func TestSecretByName(t *testing.T) {
	f := &fake{}
	resp, err := f.provider(t).Mount(ctx, mountRequest("- {secret: db, field: user}\n", nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := files(resp); got["user"] != "field-of-sec_db" {
		t.Fatal(got)
	}
	if v := versions(resp); v[0] != "sec_db/user@7" {
		t.Fatal(v)
	}
	if _, err := f.provider(t).Mount(ctx, mountRequest("- {secret: nope}\n", nil)); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
}

func TestBinaryFieldDecoded(t *testing.T) {
	f := &fake{}
	resp, err := f.provider(t).Mount(ctx, mountRequest("- secret: sec_app\n", nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := files(resp)["cert"]; got != "\x00\x01\x02" {
		t.Fatalf("%q", got)
	}
}

func TestProjectParameter(t *testing.T) {
	f := &fake{}
	_, err := f.provider(t).Mount(ctx, mountRequest("- secret: sec_app\n", map[string]string{"project": "prj_other"}))
	// The fake serves only prj_b: the parameter was used.
	if status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
}

func TestNoTokenFailsMount(t *testing.T) {
	f := &fake{}
	req := mountRequest("- secret: sec_app\n", map[string]string{"csi.storage.k8s.io/serviceAccount.tokens": `{"other":{"token":"x"}}`})
	_, err := f.provider(t).Mount(ctx, req)
	if status.Code(err) != codes.InvalidArgument || f.exchanges.Load() != 0 {
		t.Fatal(err)
	}
}

func TestRefusedExchangeFailsMount(t *testing.T) {
	f := &fake{refuse: true}
	_, err := f.provider(t).Mount(ctx, mountRequest("- secret: sec_app\n", nil))
	if status.Code(err) != codes.PermissionDenied || !strings.Contains(err.Error(), "access_denied") || !strings.Contains(err.Error(), "no pod identity association") {
		t.Fatal(err)
	}
	if f.reads.Load() != 0 {
		t.Fatal("read secrets without a key")
	}
}

func TestReadErrorKeepsCode(t *testing.T) {
	f := &fake{}
	_, err := f.provider(t).Mount(ctx, mountRequest("- secret: sec_gone\n", nil))
	if status.Code(err) != codes.NotFound || !strings.Contains(err.Error(), "not_found") {
		t.Fatal(err)
	}
}

// Nothing is kept between mounts: each one exchanges the pod's token afresh.
func TestEachMountExchanges(t *testing.T) {
	f := &fake{}
	p := f.provider(t)
	for range 2 {
		if _, err := p.Mount(ctx, mountRequest("- secret: sec_app\n", nil)); err != nil {
			t.Fatal(err)
		}
	}
	if f.exchanges.Load() != 2 {
		t.Fatal(f.exchanges.Load())
	}
}

func TestPathTraversalRefused(t *testing.T) {
	for _, objects := range []string{
		"- {secret: sec_app, field: password, path: ../x}\n",
		"- {secret: sec_app, field: password, path: /etc/x}\n",
		"- {secret: sec_app, path: a/../../x}\n",
		"- {secret: sec_app, field: a, path: same}\n- {secret: sec_b, field: b, path: same}\n",
		"- {secret: sec_app, unknown: 1}\n",
		"- {secret: sec_app, field: a, path: ./same}\n- {secret: sec_b, field: b, path: same}\n",
		"- {secret: sec_app, field: ..data}\n",
		"- {secret: sec_app, field: a, path: x/..data/y}\n",
		"- {field: password}\n",
	} {
		f := &fake{}
		if _, err := f.provider(t).Mount(ctx, mountRequest(objects, nil)); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%q: %v", objects, err)
		}
		if f.exchanges.Load() != 0 {
			t.Fatalf("%q: exchanged before checking", objects)
		}
	}
	// A field name from the API is a file name too.
	f := &fake{}
	for _, secret := range []string{"sec_evil", "sec_dots"} {
		if _, err := f.provider(t).Mount(ctx, mountRequest("- secret: "+secret+"\n", nil)); status.Code(err) != codes.InvalidArgument {
			t.Fatal(secret, err)
		}
	}
}
