package kubeclient

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// PodRef identifies a container to exec into / stream logs from / copy files to.
type PodRef struct {
	Namespace string
	Pod       string
	Container string // empty = the pod's default container
}

// ExecOptions configures a pod-exec call.
type ExecOptions struct {
	Command []string
	Stdin   io.Reader // optional
	TTY     bool
}

// ExecResult captures a pod-exec invocation's output.
type ExecResult struct {
	Stdout []byte
	Stderr []byte
}

// Error sentinels for the exec / log / copy helpers, classified by behaviour.
var (
	// ErrNoExecutor is returned when a SPDY executor cannot be constructed
	// (typically because the Client was built from a fake clientset with no
	// rest.Config — exec requires a live API server).
	ErrNoExecutor = errors.New("kubeclient: no rest.Config for pod exec")
	// ErrExecFailed wraps a remote-command stream failure.
	ErrExecFailed = errors.New("kubeclient: pod exec failed")
	// ErrEmptyCommand is returned when ExecOptions.Command is empty.
	ErrEmptyCommand = errors.New("kubeclient: exec command is empty")
)

// coreRESTClient builds a core/v1 REST client from the resolved rest.Config —
// the transport-correct way to construct an exec subresource request. It does
// not depend on the typed clientset (which a fake cannot back with a real
// transport), so exec works against any *rest.Config.
func (c *Client) coreRESTClient() (*rest.RESTClient, error) {
	cfg := rest.CopyConfig(c.Config)
	cfg.GroupVersion = &schema.GroupVersion{Group: "", Version: "v1"}
	cfg.APIPath = "/api"
	cfg.NegotiatedSerializer = rest.CodecFactoryForGeneratedClient(scheme.Scheme, scheme.Codecs).WithoutConversion()
	return rest.RESTClientFor(cfg)
}

// newSPDYExecutor is overridable in tests so exec/copy paths can be exercised
// without a live API server.
var newSPDYExecutor = func(cfg *rest.Config, method string, rawURL string) (remotecommand.Executor, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	return remotecommand.NewSPDYExecutor(cfg, method, u)
}

// Exec runs a command in a pod container and captures stdout/stderr — the typed
// replacement for shelling out to `kubectl exec`.
func (c *Client) Exec(ctx context.Context, ref PodRef, opts ExecOptions) (ExecResult, error) {
	if len(opts.Command) == 0 {
		return ExecResult{}, ErrEmptyCommand
	}
	if c.Config == nil {
		return ExecResult{}, ErrNoExecutor
	}
	rc, err := c.coreRESTClient()
	if err != nil {
		return ExecResult{}, fmt.Errorf("%w: %v", ErrNoExecutor, err)
	}
	req := rc.Post().
		Resource("pods").
		Name(ref.Pod).
		Namespace(ref.Namespace).
		SubResource("exec")
	req.VersionedParams(&corev1.PodExecOptions{
		Container: ref.Container,
		Command:   opts.Command,
		Stdin:     opts.Stdin != nil,
		Stdout:    true,
		Stderr:    true,
		TTY:       opts.TTY,
	}, scheme.ParameterCodec)

	exec, err := newSPDYExecutor(c.Config, "POST", req.URL().String())
	if err != nil {
		return ExecResult{}, fmt.Errorf("%w: %v", ErrExecFailed, err)
	}
	var stdout, stderr bytes.Buffer
	streamErr := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  opts.Stdin,
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    opts.TTY,
	})
	res := ExecResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if streamErr != nil {
		return res, fmt.Errorf("%w: %v", ErrExecFailed, streamErr)
	}
	return res, nil
}

// LogOptions configures a log stream.
type LogOptions struct {
	Follow       bool
	Previous     bool
	TailLines    *int64
	SinceSeconds *int64
}

// StreamLogs opens a log stream for a pod container and returns the reader the
// caller drains + closes — the typed replacement for `kubectl logs -f`.
func (c *Client) StreamLogs(ctx context.Context, ref PodRef, opts LogOptions) (io.ReadCloser, error) {
	req := c.Clientset.CoreV1().Pods(ref.Namespace).GetLogs(ref.Pod, &corev1.PodLogOptions{
		Container:    ref.Container,
		Follow:       opts.Follow,
		Previous:     opts.Previous,
		TailLines:    opts.TailLines,
		SinceSeconds: opts.SinceSeconds,
	})
	return req.Stream(ctx)
}

// ReadLogs is StreamLogs followed by io.ReadAll — the convenience for the common
// "give me the last N lines as bytes" case.
func (c *Client) ReadLogs(ctx context.Context, ref PodRef, opts LogOptions) ([]byte, error) {
	rc, err := c.StreamLogs(ctx, ref, opts)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// CopyToPod copies bytes into a file inside a pod container, by exec-ing `tar`
// and streaming a single-entry tar archive over stdin — the same mechanism
// `kubectl cp` uses, expressed once. destPath is absolute inside the container.
func (c *Client) CopyToPod(ctx context.Context, ref PodRef, destPath string, content []byte) error {
	archive, err := tarSingleFile(destPath, content)
	if err != nil {
		return err
	}
	_, err = c.Exec(ctx, ref, ExecOptions{
		Command: []string{"tar", "-xmf", "-", "-C", destPathRoot(destPath)},
		Stdin:   bytes.NewReader(archive),
	})
	return err
}

// tarSingleFile builds a one-entry tar archive whose single member is the
// basename of destPath (extracted relative to destPath's directory).
func tarSingleFile(destPath string, content []byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name:     path.Base(destPath),
		Mode:     0o600,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func destPathRoot(destPath string) string {
	d := path.Dir(destPath)
	if d == "" || d == "." {
		return "/"
	}
	return d
}

// GetPod is a thin typed pod fetch — the common precondition for exec/logs/copy.
func (c *Client) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	return c.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
}
