package connectors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const maxPodcastAudioBytes = 200 << 20

var carrierGradeNAT = netip.MustParsePrefix("100.64.0.0/10")

// DownloadPodcastAudio fetches one public HTTPS enclosure without credentials.
func DownloadPodcastAudio(ctx context.Context, address string, output io.Writer) error {
	dialer := net.Dialer{Timeout: 15 * time.Second, Control: checkPublicPeer}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil {
				return errors.New("unsafe podcast audio redirect")
			}
			return nil
		},
	}
	return downloadPodcastAudio(ctx, client, address, output, maxPodcastAudioBytes)
}

// Dialer supplies the resolved peer, so redirects and DNS changes receive the
// same check as an address written directly in the feed.
func checkPublicPeer(_, address string, _ syscall.RawConn) error {
	peer, err := netip.ParseAddrPort(address)
	if err != nil {
		return errors.New("network address could not be verified")
	}
	ip := peer.Addr().Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || carrierGradeNAT.Contains(ip) {
		return errors.New("source must use a public network address")
	}
	return nil
}

func downloadPodcastAudio(ctx context.Context, client *http.Client, address string, output io.Writer, limit int64) error {
	parsed, err := webURL(address)
	if err != nil || parsed.Scheme != "https" {
		return errors.New("podcast enclosure must be a public HTTPS URL without credentials")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return fmt.Errorf("build podcast audio request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		// Signed enclosure URLs can contain secrets in their query parameters.
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return fmt.Errorf("download podcast audio from %s: %w", parsed.Hostname(), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("podcast audio returned HTTP %d", response.StatusCode)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != "" && !strings.HasPrefix(mediaType, "audio/") && !strings.HasPrefix(mediaType, "video/") && mediaType != "application/octet-stream" {
		return fmt.Errorf("unexpected podcast enclosure type %s", mediaType)
	}
	if response.ContentLength > limit {
		return fmt.Errorf("podcast audio exceeds %d bytes", limit)
	}
	n, err := io.Copy(output, io.LimitReader(response.Body, limit+1))
	if err != nil {
		return fmt.Errorf("read podcast audio: %w", err)
	}
	if n == 0 || n > limit {
		return errors.New("podcast audio is empty or exceeds the download limit")
	}
	return nil
}
