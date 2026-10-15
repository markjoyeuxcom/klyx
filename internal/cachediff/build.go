package cachediff

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/moomora/klyx/internal/procrun"
)

// Bounds of one confined build.
const (
	buildDeadline = 60 * time.Second
	outputMax     = 64 << 20
	stderrMax     = 64 << 10
)

// Build runs the confined build in a child of exe - Klyx's own binary -
// under procrun: an empty environment, the input set's contents as a tar on
// stdin, the YAML stream back on stdout, a deadline and a process-group
// kill. It returns the objects built.
func Build(ctx context.Context, exe string, set InputSet, contents map[string][]byte, spec map[string]any) ([]*unstructured.Unstructured, error) {
	if !procrun.Supported() {
		return nil, procrun.ErrUnsupported
	}
	in, err := inputTar(set, contents, spec)
	if err != nil {
		return nil, err
	}
	r := procrun.Run(ctx, procrun.Spec{Exe: exe, Args: []string{ChildFlag}, Env: nil, Dir: "/", Stdin: in,
		StdoutMax: outputMax, StderrMax: stderrMax, Deadline: buildDeadline, StdinDeadline: buildDeadline, WaitDelay: 2 * time.Second})
	switch {
	case r.StartErr != nil:
		return nil, fmt.Errorf("the confined build did not start: %w", r.StartErr)
	case r.GateErr != nil:
		return nil, fmt.Errorf("the confined build did not take its input: %w", r.GateErr)
	case r.TimedOut:
		return nil, fmt.Errorf("the confined build passed %s and was stopped", buildDeadline)
	case r.StdoutOver:
		return nil, fmt.Errorf("the build's output passes %d MiB", outputMax>>20)
	case r.ExitCode != 0:
		msg := strings.TrimSpace(string(r.Stderr))
		if msg == "" {
			msg = fmt.Sprintf("exit %d", r.ExitCode)
		}
		return nil, errors.New(truncate(msg, 2000))
	}
	return decodeObjects(r.Stdout)
}

// inputTar writes the request and every file of the input set, in path
// order, as regular files under repo/.
func inputTar(set InputSet, contents map[string][]byte, spec map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	req, err := json.Marshal(buildRequest{Root: set.Root, Spec: spec})
	if err != nil {
		return nil, err
	}
	write := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := write(requestName, req); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(set.Files))
	for p := range set.Files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		data, ok := contents[p]
		if !ok {
			return nil, fmt.Errorf("%s was not read", p)
		}
		if err := write(repoDir+"/"+p, data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeObjects reads a YAML stream of Kubernetes objects.
func decodeObjects(out []byte) ([]*unstructured.Unstructured, error) {
	dec := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
	var objs []*unstructured.Unstructured
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return objs, nil
			}
			return nil, fmt.Errorf("reading the build's output: %w", err)
		}
		if len(m) == 0 {
			continue
		}
		objs = append(objs, &unstructured.Unstructured{Object: m})
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
