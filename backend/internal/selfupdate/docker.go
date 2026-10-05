package selfupdate

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// dockerClient is a minimal Docker Engine API client speaking over the unix
// socket. It implements only the handful of endpoints the self-update flow
// needs; the docker SDK is deliberately not imported to keep the binary lean.
type dockerClient struct {
	sock string
	http *http.Client
	auth string // base64 X-Registry-Auth payload, empty when no credentials
}

var errDockerNotFound = errors.New("docker: not found")

func newDockerClient(sock string) *dockerClient {
	return &dockerClient{
		sock: sock,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", sock)
				},
				DisableCompression: true,
			},
		},
	}
}

func (d *dockerClient) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if d.auth != "" {
		req.Header.Set("X-Registry-Auth", d.auth)
	}
	return d.http.Do(req)
}

func (d *dockerClient) getJSON(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := d.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errDockerNotFound
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("docker %s: HTTP %d: %s", path, resp.StatusCode, limitBody(resp.Body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type containerInspect struct {
	ID              string           `json:"Id"`
	Name            string           `json:"Name"`
	Image           string           `json:"Image"`
	Mounts          []containerMount `json:"Mounts"`
	Config          *containerConfig `json:"Config"`
	HostConfig      *hostConfig      `json:"HostConfig"`
	NetworkSettings *networkSettings `json:"NetworkSettings"`
}

type containerConfig struct {
	Image       string            `json:"Image"`
	Env         []string          `json:"Env"`
	Labels      map[string]string `json:"Labels"`
	Healthcheck *healthcheck      `json:"Healthcheck"`
}

type hostConfig struct {
	Binds         []string       `json:"Binds"`
	NetworkMode   string         `json:"NetworkMode"`
	ExtraHosts    []string       `json:"ExtraHosts"`
	RestartPolicy *restartPolicy `json:"RestartPolicy"`
	LogConfig     *logConfig     `json:"LogConfig"`
}

// containerMount is the inspect view of a mount; named volumes need to be
// recreated explicitly or the peer container loses them (binds ride along in
// HostConfig.Binds already).
type containerMount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name,omitempty"`
	Destination string `json:"Destination"`
	ReadOnly    bool   `json:"RW"`
}

type healthcheck struct {
	Test        []string `json:"Test"`
	Interval    int64    `json:"Interval,omitempty"`
	Timeout     int64    `json:"Timeout,omitempty"`
	StartPeriod int64    `json:"StartPeriod,omitempty"`
	Retries     int      `json:"Retries,omitempty"`
}

type restartPolicy struct {
	Name string `json:"Name"`
}

type logConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config"`
}

type networkSettings struct {
	Networks map[string]endpointSettings `json:"Networks"`
}

type endpointSettings struct {
	Aliases []string `json:"Aliases"`
}

func (d *dockerClient) inspectContainer(ctx context.Context, id string) (*containerInspect, error) {
	var out containerInspect
	if err := d.getJSON(ctx, "/containers/"+id+"/json", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type imageInspect struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
}

func (d *dockerClient) inspectImage(ctx context.Context, ref string) (*imageInspect, error) {
	var out imageInspect
	if err := d.getJSON(ctx, "/images/"+ref+"/json", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// pullImage pulls repo:tag through the daemon and drains the progress stream,
// forwarding each progress line to onLine.
func (d *dockerClient) pullImage(ctx context.Context, repo, tag string, onLine func(string)) error {
	q := url.Values{}
	q.Set("fromImage", repo)
	q.Set("tag", tag)
	resp, err := d.do(ctx, http.MethodPost, "/images/create?"+q.Encode(), nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("pull %s:%s: HTTP %d: %s", repo, tag, resp.StatusCode, limitBody(resp.Body))
	}
	dec := json.NewDecoder(resp.Body)
	var line struct {
		Status      string `json:"status"`
		Progress    string `json:"progress"`
		ID          string `json:"id"`
		ErrorDetail *struct {
			Message string `json:"message"`
		} `json:"errorDetail"`
	}
	for dec.More() {
		if err := dec.Decode(&line); err != nil {
			return fmt.Errorf("pull progress: %w", err)
		}
		if line.ErrorDetail != nil {
			return fmt.Errorf("pull: %s", line.ErrorDetail.Message)
		}
		if onLine != nil && line.ID != "" && (line.Status != "" || line.Progress != "") {
			onLine(line.ID + ": " + line.Status + " " + line.Progress)
		}
	}
	return ctx.Err()
}

type createRequest struct {
	Image       string            `json:"Image"`
	Env         []string          `json:"Env,omitempty"`
	Labels      map[string]string `json:"Labels,omitempty"`
	Healthcheck *healthcheck      `json:"Healthcheck,omitempty"`
	HostConfig  *createHostConfig `json:"HostConfig,omitempty"`
}

type createHostConfig struct {
	Binds         []string       `json:"Binds,omitempty"`
	Mounts        []mountConfig  `json:"Mounts,omitempty"`
	NetworkMode   string         `json:"NetworkMode,omitempty"`
	ExtraHosts    []string       `json:"ExtraHosts,omitempty"`
	RestartPolicy *restartPolicy `json:"RestartPolicy,omitempty"`
	LogConfig     *logConfig     `json:"LogConfig,omitempty"`
}

type mountConfig struct {
	Type     string `json:"Type"`
	Source   string `json:"Source,omitempty"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly,omitempty"`
}

func (d *dockerClient) createContainer(ctx context.Context, name string, req *createRequest) (string, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := d.do(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), bytes.NewReader(raw), "application/json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		ID       string   `json:"Id"`
		Warnings []string `json:"Warnings"`
		Message  string   `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil && resp.StatusCode < 300 {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("create %s: HTTP %d: %s", name, resp.StatusCode, firstNonEmpty(out.Message, limitBody(resp.Body)))
	}
	if out.ID == "" {
		return "", fmt.Errorf("create %s: empty container id", name)
	}
	return out.ID, nil
}

func (d *dockerClient) startContainer(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := d.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		return fmt.Errorf("start %s: HTTP %d: %s", id, resp.StatusCode, limitBody(resp.Body))
	}
	return nil
}

// stopContainer asks the daemon to send SIGTERM and wait up to timeoutSeconds
// before SIGKILL. When stopping the calling container itself, the call never
// returns because the process is being torn down — that is expected.
func (d *dockerClient) stopContainer(ctx context.Context, id string, timeoutSeconds int) error {
	resp, err := d.do(ctx, http.MethodPost, "/containers/"+id+"/stop?t="+strconv.Itoa(timeoutSeconds), nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		return fmt.Errorf("stop %s: HTTP %d: %s", id, resp.StatusCode, limitBody(resp.Body))
	}
	return nil
}

func (d *dockerClient) removeContainer(ctx context.Context, name string, force bool) error {
	q := url.Values{}
	q.Set("v", "false")
	if force {
		q.Set("force", "true")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := d.do(ctx, http.MethodDelete, "/containers/"+name+"?"+q.Encode(), nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("remove %s: HTTP %d: %s", name, resp.StatusCode, limitBody(resp.Body))
	}
	return nil
}

// execInContainer runs a command inside the given container and returns its
// combined output and exit code.
func (d *dockerClient) execInContainer(ctx context.Context, container string, cmd []string) (string, int, error) {
	createBody, _ := json.Marshal(struct {
		Cmd          []string `json:"Cmd"`
		AttachStdout bool     `json:"AttachStdout"`
		AttachStderr bool     `json:"AttachStderr"`
	}{Cmd: cmd, AttachStdout: true, AttachStderr: true})
	resp, err := d.do(ctx, http.MethodPost, "/containers/"+container+"/exec", bytes.NewReader(createBody), "application/json")
	if err != nil {
		return "", -1, err
	}
	var created struct {
		ID      string `json:"Id"`
		Message string `json:"message"`
	}
	err = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if err != nil {
		return "", -1, err
	}
	if resp.StatusCode >= 300 || created.ID == "" {
		return "", -1, fmt.Errorf("exec create in %s: HTTP %d: %s", container, resp.StatusCode, firstNonEmpty(created.Message, "no exec id"))
	}

	startBody, _ := json.Marshal(struct {
		Detach bool `json:"Detach"`
		Tty    bool `json:"Tty"`
	}{Detach: false, Tty: false})
	startResp, err := d.do(ctx, http.MethodPost, "/exec/"+created.ID+"/start", bytes.NewReader(startBody), "application/json")
	if err != nil {
		return "", -1, err
	}
	defer startResp.Body.Close()
	if startResp.StatusCode >= 300 {
		return "", -1, fmt.Errorf("exec start %s: HTTP %d: %s", created.ID, startResp.StatusCode, limitBody(startResp.Body))
	}
	var output bytes.Buffer
	if err := demuxDockerStream(startResp.Body, &output); err != nil {
		return output.String(), -1, fmt.Errorf("exec output: %w", err)
	}
	var state struct {
		ExitCode int `json:"ExitCode"`
	}
	if err := d.getJSON(ctx, "/exec/"+created.ID+"/json", &state); err != nil {
		return output.String(), -1, err
	}
	return output.String(), state.ExitCode, nil
}

// demuxDockerStream unwraps the engine's 8-byte-framed stdout/stderr stream
// into raw output. Non-framed bodies (tty mode) pass through untouched.
func demuxDockerStream(r io.Reader, w io.Writer) error {
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if err == io.EOF {
				return nil
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				_, _ = w.Write(header)
				_, err = io.Copy(w, r)
				return err
			}
			return err
		}
		if header[0] > 2 {
			_, _ = w.Write(header)
			_, err := io.Copy(w, r)
			return err
		}
		if _, err := io.CopyN(w, r, int64(binary.BigEndian.Uint32(header[4:8]))); err != nil {
			return err
		}
	}
}

func limitBody(r io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(r, 512))
	if err != nil {
		return ""
	}
	return strings.Map(func(c rune) rune {
		if c >= 0x20 && c != 0x7f {
			return c
		}
		return -1
	}, string(raw))
}
