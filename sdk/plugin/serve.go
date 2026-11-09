// Package plugin is the Go side of the experimental plugin protocol: Serve for
// a plugin's main function, and Host for a program that starts plugins.
//
// A plugin is an executable that opens a Unix socket, prints one handshake line
// on stdout, and answers the protocol's HTTP RPC methods until its stdin closes.
package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	"google.golang.org/protobuf/proto"
)

// ProtocolMajor is the revision of the protocol this package speaks.
const ProtocolMajor = 1

const handshakePrefix = "herdsman-plugin/"

// Protocol is the version string of this experimental surface.
func Protocol() string { return handshakePrefix + strconv.Itoa(ProtocolMajor) }

// shutdownWait bounds how long calls in flight may finish once a plugin stops.
const shutdownWait = 2 * time.Second

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidName says whether name can name a plugin: it becomes part of a file
// path, so it can hold no separator.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Handshake is the parsed form of a plugin's first line.
type Handshake struct {
	Major    int
	Socket   string
	Checksum string
}

// FormatHandshake writes the line for the current protocol, without a newline.
func FormatHandshake(socket, checksum string) string {
	return Protocol() + " " + socket + " " + checksum
}

// ParseHandshake reads a handshake line. A protocol major this package does not
// speak still parses, so that the caller can report which one it was.
func ParseHandshake(line string) (Handshake, error) {
	fields := strings.Split(line, " ")
	if len(fields) != 3 {
		return Handshake{}, fmt.Errorf("handshake line %q: want three fields", line)
	}
	digits, ok := strings.CutPrefix(fields[0], handshakePrefix)
	if !ok {
		return Handshake{}, fmt.Errorf("handshake line %q: want the prefix %s", line, handshakePrefix)
	}
	major, err := strconv.ParseUint(digits, 10, 16)
	if err != nil {
		return Handshake{}, fmt.Errorf("handshake line %q: bad protocol major", line)
	}
	if !filepath.IsAbs(fields[1]) {
		return Handshake{}, fmt.Errorf("handshake line %q: the socket path is not absolute", line)
	}
	if decoded, err := hex.DecodeString(fields[2]); err != nil || len(decoded) != sha256.Size {
		return Handshake{}, fmt.Errorf("handshake line %q: bad manifest checksum", line)
	}
	return Handshake{Major: int(major), Socket: fields[1], Checksum: fields[2]}, nil
}

// ManifestChecksum is the SHA-256, in hex, of the manifest's deterministic
// encoding.
func ManifestChecksum(manifest *pluginv1.Manifest) (string, error) {
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(manifest)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Extensions holds a plugin's implementations; the ones it has must be exactly
// the extension points its manifest declares.
type Extensions struct {
	ConfigCodec      pluginv1.ConfigCodecService
	ResourceDetector pluginv1.ResourceDetectorService
	LandingBackend   pluginv1.LandingBackendService
	MessageGenerator pluginv1.MessageGeneratorService
	ListColumn       pluginv1.ListColumnService
}

// extension is one extension point of a plugin: whether it is implemented, and
// its methods. The methods go through package rpc, which keeps a failure's code;
// the handlers the generator writes report every failure as unknown.
type extension struct {
	present    bool
	procedures func() []rpc.Procedure
}

func (e Extensions) mounted() map[pluginv1.ExtensionPoint]extension {
	return map[pluginv1.ExtensionPoint]extension{
		pluginv1.ExtensionPoint_EXTENSION_POINT_CONFIG_CODEC: {e.ConfigCodec != nil, func() []rpc.Procedure {
			return []rpc.Procedure{
				rpc.Unary(pluginv1.ConfigCodecServiceName, "Decode", e.ConfigCodec.Decode),
				rpc.Unary(pluginv1.ConfigCodecServiceName, "Encode", e.ConfigCodec.Encode),
				rpc.Unary(pluginv1.ConfigCodecServiceName, "Set", e.ConfigCodec.Set),
			}
		}},
		pluginv1.ExtensionPoint_EXTENSION_POINT_RESOURCE_DETECTOR: {e.ResourceDetector != nil, func() []rpc.Procedure {
			return []rpc.Procedure{rpc.Unary(pluginv1.ResourceDetectorServiceName, "Detect", e.ResourceDetector.Detect)}
		}},
		pluginv1.ExtensionPoint_EXTENSION_POINT_LANDING_BACKEND: {e.LandingBackend != nil, func() []rpc.Procedure {
			return []rpc.Procedure{
				rpc.Unary(pluginv1.LandingBackendServiceName, "Open", e.LandingBackend.Open),
				rpc.Unary(pluginv1.LandingBackendServiceName, "Merge", e.LandingBackend.Merge),
				rpc.Unary(pluginv1.LandingBackendServiceName, "Status", e.LandingBackend.Status),
			}
		}},
		pluginv1.ExtensionPoint_EXTENSION_POINT_MESSAGE_GENERATOR: {e.MessageGenerator != nil, func() []rpc.Procedure {
			return []rpc.Procedure{rpc.Unary(pluginv1.MessageGeneratorServiceName, "Generate", e.MessageGenerator.Generate)}
		}},
		pluginv1.ExtensionPoint_EXTENSION_POINT_LIST_COLUMN: {e.ListColumn != nil, func() []rpc.Procedure {
			return []rpc.Procedure{rpc.Unary(pluginv1.ListColumnServiceName, "Compute", e.ListColumn.Compute)}
		}},
	}
}

// withMajor returns a copy of the manifest carrying this package's protocol
// major when it names none.
func withMajor(manifest *pluginv1.Manifest) *pluginv1.Manifest {
	out := proto.CloneOf(manifest)
	if out.GetProtocolMajor() == 0 {
		out.ProtocolMajor = ProtocolMajor
	}
	return out
}

func validate(manifest *pluginv1.Manifest, ext Extensions) error {
	if manifest == nil {
		return errors.New("plugin: no manifest")
	}
	if !ValidName(manifest.GetName()) {
		return fmt.Errorf("plugin: invalid name %q", manifest.GetName())
	}
	if manifest.GetVersion() == "" {
		return errors.New("plugin: the manifest has no version")
	}
	if major := manifest.GetProtocolMajor(); major != 0 && major != ProtocolMajor {
		return fmt.Errorf("plugin: this SDK speaks protocol major %d, not %d", ProtocolMajor, major)
	}
	points := manifest.GetExtensionPoints()
	if len(points) == 0 {
		return errors.New("plugin: the manifest declares no extension point")
	}
	mounted := ext.mounted()
	for i, point := range points {
		entry, known := mounted[point]
		if !known {
			return fmt.Errorf("plugin: unknown extension point %s", point)
		}
		if slices.Contains(points[:i], point) {
			return fmt.Errorf("plugin: %s is declared twice", point)
		}
		if !entry.present {
			return fmt.Errorf("plugin: %s is declared but not implemented", point)
		}
	}
	for point, entry := range mounted {
		if entry.present && !slices.Contains(points, point) {
			return fmt.Errorf("plugin: %s is implemented but not declared", point)
		}
	}
	return nil
}

// manifestServer answers ManifestService.
type manifestServer struct{ manifest *pluginv1.Manifest }

func (s manifestServer) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: s.manifest}, nil
}

// Serve runs a plugin: it opens the socket, prints the handshake line on stdout
// and answers calls until ctx ends or stdin closes, which is how the host says
// it is done.
func Serve(ctx context.Context, manifest *pluginv1.Manifest, ext Extensions) error {
	return serve(ctx, manifest, ext, os.Stdin, os.Stdout)
}

func serve(ctx context.Context, manifest *pluginv1.Manifest, ext Extensions, stdin io.Reader, stdout io.Writer) error {
	if err := validate(manifest, ext); err != nil {
		return err
	}
	manifest = withMajor(manifest)
	checksum, err := ManifestChecksum(manifest)
	if err != nil {
		return err
	}
	// A short name under the temporary folder: socket paths have a low limit.
	// MkdirTemp makes it private to this user.
	dir, err := os.MkdirTemp("", "hp")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}

	procedures := []rpc.Procedure{rpc.Unary(pluginv1.ManifestServiceName, "GetManifest", manifestServer{manifest}.GetManifest)}
	mounted := ext.mounted()
	for _, point := range manifest.GetExtensionPoints() {
		procedures = append(procedures, mounted[point].procedures()...)
	}
	server := &http.Server{Handler: rpc.NewMux(procedures...), ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()

	if _, err := fmt.Fprintln(stdout, FormatHandshake(socket, checksum)); err != nil {
		_ = server.Close()
		return err
	}

	closed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, stdin)
		close(closed)
	}()
	select {
	case <-ctx.Done():
	case <-closed:
	case err := <-served:
		return err
	}
	stop, cancel := context.WithTimeout(context.Background(), shutdownWait)
	defer cancel()
	if err := server.Shutdown(stop); err != nil {
		_ = server.Close()
	}
	return nil
}
