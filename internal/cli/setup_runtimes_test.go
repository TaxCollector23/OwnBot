package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJanDiscoveryRequiresLocalModelMetadataAndKeepsAPIKeyPrivate(t *testing.T) {
	janData := t.TempDir()
	modelDir := filepath.Join(janData, "llamacpp", "models", "jan", "local-model")
	if err := os.MkdirAll(modelDir, 0700); err != nil {
		t.Fatal(err)
	}
	metadata := "model_path: llamacpp/models/jan/local-model/model.gguf\nname: local-model\nsize_bytes: 2300000000\n"
	if err := os.WriteFile(filepath.Join(modelDir, "model.yml"), []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "model.gguf"), []byte("local-weights"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JAN_DATA_DIR", janData)
	const key = "jan-local-test-key"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Errorf("Jan authorization header missing")
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"local-model"},{"id":"openai/gpt-example"}]}`)
	}))
	defer api.Close()
	models := detectLocalModelsAt(context.Background(), api.Client(), []localEndpoint{{Runtime: "Jan", Provider: "jan", BaseURL: api.URL + "/v1", APIKey: key, Jan: true}})
	if len(models) != 1 || models[0].ID != "local-model" || !models[0].LocalVerified {
		t.Fatalf("unverified or cloud model was included: %+v", models)
	}
	if models[0].SizeBytes != 2300000000 {
		t.Fatalf("local metadata size not returned: %+v", models[0])
	}
}

func TestJanCloudModelIsNotLocallyClassified(t *testing.T) {
	if _, ok := janInstalledModelInfo("openai/gpt-example", []string{t.TempDir()}); ok {
		t.Fatal("cloud model without Jan local metadata was accepted")
	}
}

func TestJanMetadataDoesNotTrustMissingEscapingOrSymlinkWeights(t *testing.T) {
	root := t.TempDir()
	metadataDir := filepath.Join(root, "llamacpp", "models", "local")
	if err := os.MkdirAll(metadataDir, 0700); err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(metadataDir, "model.yml")
	if err := os.WriteFile(metadata, []byte("name: local\nmodel_path: missing.gguf\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if janModelWeightsPresent(root, metadata, "missing.gguf") {
		t.Fatal("missing model weights were trusted")
	}
	outside := filepath.Join(t.TempDir(), "outside.gguf")
	if err := os.WriteFile(outside, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if janModelWeightsPresent(root, metadata, outside) {
		t.Fatal("weights outside the Jan data directory were trusted")
	}
	link := filepath.Join(metadataDir, "weights.gguf")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if janModelWeightsPresent(root, metadata, "weights.gguf") {
		t.Fatal("symlinked model weights were trusted")
	}
}

func TestRuntimeModelRequestRecognizesAuthRequiredWithoutPrintingCredential(t *testing.T) {
	const key = "runtime-secret-value"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Errorf("local runtime auth missing")
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer api.Close()
	status, err := getLocalJSONStatus(context.Background(), api.Client(), api.URL, key, &map[string]any{})
	if status != http.StatusUnauthorized || err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("status=%d err=%v", status, err)
	}
}

func TestJanInstalledMetadataDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	modelDir := filepath.Join(outside, "model")
	if err := os.MkdirAll(modelDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "model.yml"), []byte("name: hidden-model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "llamacpp", "models"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "llamacpp", "models", "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	info := janInstalledModelIndex([]string{root})
	if _, ok := info["hidden-model"]; ok {
		t.Fatal("Jan metadata scan followed a symlink outside the configured data folder")
	}
}
