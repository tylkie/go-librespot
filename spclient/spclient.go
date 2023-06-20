package spclient

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"github.com/cenkalti/backoff/v4"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	pb "go-librespot/proto/spotify/connectstate/model"
	storagepb "go-librespot/proto/spotify/download"
	metadatapb "go-librespot/proto/spotify/metadata"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"net/url"
)

type Spclient struct {
	client *http.Client

	baseUrl     *url.URL
	clientToken string
	deviceId    string

	accessToken librespot.GetLogin5TokenFunc
}

func NewSpclient(addr librespot.GetAddressFunc, accessToken librespot.GetLogin5TokenFunc, deviceId, clientToken string) (*Spclient, error) {
	baseUrl, err := url.Parse(fmt.Sprintf("https://%s/", addr()))
	if err != nil {
		return nil, fmt.Errorf("invalid spclient base url: %w", err)
	}

	return &Spclient{
		client:      &http.Client{},
		baseUrl:     baseUrl,
		clientToken: clientToken,
		deviceId:    deviceId,
		accessToken: accessToken,
	}, nil
}

func (c *Spclient) request(method string, path string, header http.Header, body []byte) (*http.Response, error) {
	accessToken, err := c.accessToken()
	if err != nil {
		return nil, fmt.Errorf("failed obtaining spclient access token: %w", err)
	}

	req := &http.Request{
		URL:    c.baseUrl.JoinPath(path),
		Method: method,
		Header: http.Header{},
	}

	if header != nil {
		for name, values := range header {
			req.Header[name] = values
		}
	}

	req.Header.Set("Client-Token", c.clientToken)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))

	if body != nil {
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
		req.Body, _ = req.GetBody()
	}

	resp, err := backoff.RetryWithData(func() (*http.Response, error) { return c.client.Do(req) }, backoff.NewExponentialBackOff())
	if err != nil {
		return nil, fmt.Errorf("spclient request failed: %w", err)
	}

	return resp, nil
}

func (c *Spclient) PutConnectState(spotConnId string, reqProto *pb.PutStateRequest) error {
	reqBody, err := proto.Marshal(reqProto)
	if err != nil {
		return fmt.Errorf("failed marshalling PutStateRequest: %w", err)
	}

	resp, err := c.request(
		"PUT",
		fmt.Sprintf("/connect-state/v1/devices/%s", c.deviceId),
		http.Header{
			"X-Spotify-Connection-Id": []string{spotConnId},
			"Content-Type":            []string{"application/x-protobuf"},
		},
		reqBody,
	)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 413 {
		return fmt.Errorf("connect state put request too big: %d bytes", len(reqBody))
	} else if resp.StatusCode != 200 {
		return fmt.Errorf("invalid status code from connect state put request: %d", resp.StatusCode)
	} else {
		log.Debugf("put connect state at %d because %s", reqProto.ClientSideTimestamp, reqProto.PutStateReason)
		return nil
	}
}

func (c *Spclient) ResolveStorageInteractive(fileId []byte, prefetch bool) (*storagepb.StorageResolveResponse, error) {
	var path string
	if prefetch {
		path = fmt.Sprintf("/storage-resolve/files/audio/interactive_prefetch/%s", hex.EncodeToString(fileId))
	} else {
		path = fmt.Sprintf("/storage-resolve/files/audio/interactive/%s", hex.EncodeToString(fileId))
	}

	resp, err := c.request("GET", path, nil, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid status code from storage resolve: %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var protoResp storagepb.StorageResolveResponse
	if err := proto.Unmarshal(respBytes, &protoResp); err != nil {
		return nil, fmt.Errorf("failed unmarshalling StorageResolveResponse: %w", err)
	}

	return &protoResp, nil
}

func (c *Spclient) MetadataForTrack(track librespot.TrackId) (*metadatapb.Track, error) {
	resp, err := c.request("GET", fmt.Sprintf("/metadata/4/track/%s", track.Hex()), nil, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid status code from track metadata: %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var protoResp metadatapb.Track
	if err := proto.Unmarshal(respBytes, &protoResp); err != nil {
		return nil, fmt.Errorf("failed unmarshalling Track: %w", err)
	}

	return &protoResp, nil
}
