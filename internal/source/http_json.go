package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type httpJSONRequest struct {
	Method         string
	URL            string
	Headers        map[string]string
	Body           io.Reader
	ExpectedStatus int
	RequestError   string
	StatusError    string
}

func doJSON[T any](ctx context.Context, client *http.Client, req httpJSONRequest) (T, error) {
	var out T
	if err := doJSONInto(ctx, client, req, &out); err != nil {
		return out, err
	}
	return out, nil
}

func doJSONInto(ctx context.Context, client *http.Client, spec httpJSONRequest, out any) error {
	body, err := doRequest(ctx, client, spec)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	return nil
}

func doRequest(ctx context.Context, client *http.Client, spec httpJSONRequest) ([]byte, error) {
	method := spec.Method
	if method == "" {
		method = http.MethodGet
	}
	expectedStatus := spec.ExpectedStatus
	if expectedStatus == 0 {
		expectedStatus = http.StatusOK
	}

	req, err := http.NewRequestWithContext(ctx, method, spec.URL, spec.Body)
	if err != nil {
		return nil, err
	}
	for key, value := range spec.Headers {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		if spec.RequestError != "" {
			return nil, fmt.Errorf("%s: %w", spec.RequestError, err)
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != expectedStatus {
		body, _ := io.ReadAll(resp.Body)
		if len(body) > 0 && spec.StatusError != "" {
			return nil, fmt.Errorf("%s %d: %s", spec.StatusError, resp.StatusCode, string(body))
		}
		if spec.StatusError != "" {
			return nil, fmt.Errorf("%s %d", spec.StatusError, resp.StatusCode)
		}
		if len(body) > 0 {
			return nil, fmt.Errorf("%s returned %d: %s", req.URL.Path, resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("%s returned %d", req.URL.Path, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return body, nil
}
