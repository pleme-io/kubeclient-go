package kubeclient

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// fakeExecutor is a remotecommand.Executor stand-in that writes canned output
// and records what stdin it received, so exec / copy paths run without a live
// API server.
type fakeExecutor struct {
	stdout    string
	stderr    string
	gotStdin  []byte
	streamErr error
}

func (f *fakeExecutor) Stream(opts remotecommand.StreamOptions) error {
	return f.StreamWithContext(context.Background(), opts)
}

func (f *fakeExecutor) StreamWithContext(_ context.Context, opts remotecommand.StreamOptions) error {
	if opts.Stdin != nil {
		f.gotStdin, _ = io.ReadAll(opts.Stdin)
	}
	if opts.Stdout != nil && f.stdout != "" {
		_, _ = opts.Stdout.Write([]byte(f.stdout))
	}
	if opts.Stderr != nil && f.stderr != "" {
		_, _ = opts.Stderr.Write([]byte(f.stderr))
	}
	return f.streamErr
}

// withFakeExecutor swaps the SPDY-executor seam for the duration of a test.
func withFakeExecutor(t *testing.T, fe *fakeExecutor) {
	t.Helper()
	orig := newSPDYExecutor
	newSPDYExecutor = func(_ *rest.Config, _ string, _ string) (remotecommand.Executor, error) {
		return fe, nil
	}
	t.Cleanup(func() { newSPDYExecutor = orig })
}

func execTestClient() *Client {
	cs := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
	})
	c := FromClients(cs, nil)
	c.Config = &rest.Config{Host: "https://api"} // exec requires a non-nil rest.Config
	return c
}

func TestExec(t *testing.T) {
	tests := []struct {
		name       string
		opts       ExecOptions
		fe         *fakeExecutor
		wantStdout string
		wantErr    error
	}{
		{
			name:       "captures stdout",
			opts:       ExecOptions{Command: []string{"echo", "hi"}},
			fe:         &fakeExecutor{stdout: "hi\n"},
			wantStdout: "hi\n",
		},
		{
			name:    "empty command rejected",
			opts:    ExecOptions{Command: nil},
			fe:      &fakeExecutor{},
			wantErr: ErrEmptyCommand,
		},
		{
			name:    "stream failure wrapped",
			opts:    ExecOptions{Command: []string{"false"}},
			fe:      &fakeExecutor{streamErr: errors.New("boom")},
			wantErr: ErrExecFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withFakeExecutor(t, tt.fe)
			c := execTestClient()
			res, err := c.Exec(context.Background(), PodRef{Namespace: "ns", Pod: "p"}, tt.opts)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if string(res.Stdout) != tt.wantStdout {
				t.Fatalf("stdout = %q, want %q", res.Stdout, tt.wantStdout)
			}
		})
	}
}

func TestExecNoConfig(t *testing.T) {
	c := FromClients(fake.NewSimpleClientset(), nil) // no rest.Config
	_, err := c.Exec(context.Background(), PodRef{Namespace: "ns", Pod: "p"},
		ExecOptions{Command: []string{"echo"}})
	if !errors.Is(err, ErrNoExecutor) {
		t.Fatalf("err = %v, want ErrNoExecutor", err)
	}
}

func TestCopyToPod(t *testing.T) {
	fe := &fakeExecutor{}
	withFakeExecutor(t, fe)
	c := execTestClient()
	content := []byte("hello world")
	err := c.CopyToPod(context.Background(), PodRef{Namespace: "ns", Pod: "p"},
		"/etc/app/config.yaml", content)
	if err != nil {
		t.Fatalf("CopyToPod: %v", err)
	}
	// The fake executor should have received a tar archive on stdin containing
	// the file's basename + content.
	if len(fe.gotStdin) == 0 {
		t.Fatal("no tar archive streamed to stdin")
	}
	if !strings.Contains(string(fe.gotStdin), "config.yaml") {
		t.Fatalf("tar archive missing basename; got %q", fe.gotStdin)
	}
	if !strings.Contains(string(fe.gotStdin), "hello world") {
		t.Fatalf("tar archive missing content; got %q", fe.gotStdin)
	}
}

func TestStreamLogs(t *testing.T) {
	// The fake clientset returns a canned "fake logs" stream for GetLogs.
	c := FromClients(fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
	}), nil)
	b, err := c.ReadLogs(context.Background(), PodRef{Namespace: "ns", Pod: "p"}, LogOptions{})
	if err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("expected non-empty fake logs")
	}
}

func TestTarSingleFile(t *testing.T) {
	archive, err := tarSingleFile("/a/b/c.txt", []byte("data"))
	if err != nil {
		t.Fatalf("tarSingleFile: %v", err)
	}
	if !strings.Contains(string(archive), "c.txt") {
		t.Fatalf("archive missing basename: %q", archive)
	}
	if got := destPathRoot("/a/b/c.txt"); got != "/a/b" {
		t.Fatalf("destPathRoot = %q, want /a/b", got)
	}
	if got := destPathRoot("c.txt"); got != "/" {
		t.Fatalf("destPathRoot(bare) = %q, want /", got)
	}
}
