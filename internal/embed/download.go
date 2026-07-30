package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// hfBase is the HuggingFace resolve host. Only public model files are fetched.
const hfBase = "https://huggingface.co"

// downloadClient fetches model files. It sets no overall timeout because model
// weights can be large; callers bound the work by cancelling the context.
var downloadClient = &http.Client{}

// hfModelInfo is the subset of the HuggingFace model API response we read.
type hfModelInfo struct {
	Siblings []struct {
		RFilename string `json:"rfilename"`
	} `json:"siblings"`
}

// downloadModel fetches the minimal file set needed to run modelID's feature
// extraction pipeline into destDir: the requested onnxFile (plus its external
// data file if the repo has one) and the model's config and tokenizer files.
// Files already present are skipped. It deliberately avoids hugot's hub
// downloader, which leaves lock files open on Windows and aborts large
// downloads.
func downloadModel(ctx context.Context, client *http.Client, modelID, destDir, onnxFile string) error {
	files, err := listRepoFiles(ctx, client, modelID)
	if err != nil {
		return err
	}

	present := make(map[string]bool, len(files))
	for _, f := range files {
		present[f] = true
	}
	if onnxFile != "" && !present[onnxFile] {
		return fmt.Errorf("onnx file %q not found in %s; available: %s",
			onnxFile, modelID, strings.Join(onnxVariants(files), ", "))
	}

	for _, rel := range wantedFiles(files, onnxFile) {
		if err := downloadFile(ctx, client, modelID, rel, destDir); err != nil {
			return fmt.Errorf("download %s: %w", rel, err)
		}
	}
	return nil
}

// listRepoFiles returns every file path in modelID's repository.
func listRepoFiles(ctx context.Context, client *http.Client, modelID string) ([]string, error) {
	url := hfBase + "/api/models/" + modelID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build model-info request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch model info: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model info for %q returned %s", modelID, resp.Status)
	}

	var info hfModelInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode model info: %w", err)
	}
	out := make([]string, len(info.Siblings))
	for i, s := range info.Siblings {
		out[i] = s.RFilename
	}
	return out, nil
}

// wantedFiles selects the files to download: the requested onnx file (and its
// optional external-data companion) plus the small config and tokenizer files
// (repo-root .json/.txt and the sentence-transformers pooling config). Large
// alternative weight formats (safetensors, PyTorch, TF, OpenVINO, other onnx
// variants) are skipped.
func wantedFiles(all []string, onnxFile string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(f string) {
		if f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}

	add(onnxFile)
	present := map[string]bool{}
	for _, f := range all {
		present[f] = true
	}
	if onnxFile != "" && present[onnxFile+"_data"] {
		add(onnxFile + "_data")
	}

	for _, f := range all {
		if strings.Contains(f, "/") {
			if f == "1_Pooling/config.json" {
				add(f)
			}
			continue
		}
		switch strings.ToLower(filepath.Ext(f)) {
		case ".json", ".txt":
			add(f)
		}
	}
	return out
}

// onnxVariants returns the repo's .onnx files, for error messages.
func onnxVariants(all []string) []string {
	var out []string
	for _, f := range all {
		if strings.HasSuffix(strings.ToLower(f), ".onnx") {
			out = append(out, f)
		}
	}
	return out
}

// downloadFile streams a single repo file to destDir/rel, creating parent
// directories. It skips a file that already exists with non-zero size, and
// writes through a temporary file so an interrupted download is never mistaken
// for a complete one.
func downloadFile(ctx context.Context, client *http.Client, modelID, rel, destDir string) error {
	dst := filepath.Join(destDir, filepath.FromSlash(rel))
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	url := hfBase + "/" + modelID + "/resolve/main/" + pathEscape(rel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("returned %s", resp.Status)
	}

	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("finalize: %w", err)
	}
	return nil
}

// pathEscape percent-escapes each path segment while preserving the slashes.
func pathEscape(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return path.Join(parts...)
}
