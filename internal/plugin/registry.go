package plugin

import (
	"fmt"
	"slices"

	sdkplugin "github.com/arafathusayn/herdsman/sdk/plugin"
	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
)

// Skipped is a plugin that could not be used, and why.
type Skipped struct {
	Name   string
	Reason string
}

// builtin is one built-in's manifest and its in-process clients: the same two
// things a started plugin has.
type builtin struct {
	manifest *pluginv1.Manifest
	clients  sdkplugin.Clients
}

// Registry holds the codecs, detectors, backends, generators and columns. main
// builds it once; built-ins and plugins sit behind the same interfaces.
type Registry struct {
	codecs     map[string]pluginv1.ConfigCodecService
	extensions map[string]string
	detectors  map[string]pluginv1.ResourceDetectorService
	backends   map[string]pluginv1.LandingBackendService
	generators map[string]pluginv1.MessageGeneratorService
	columns    map[string]pluginv1.ListColumnService
	builtins   map[string]*builtin
	processes  []*sdkplugin.Process
	skipped    []Skipped
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		codecs:     map[string]pluginv1.ConfigCodecService{},
		extensions: map[string]string{},
		detectors:  map[string]pluginv1.ResourceDetectorService{},
		backends:   map[string]pluginv1.LandingBackendService{},
		generators: map[string]pluginv1.MessageGeneratorService{},
		columns:    map[string]pluginv1.ListColumnService{},
		builtins:   map[string]*builtin{},
	}
}

// add stores a built-in under its name; two built-ins may not share one for the
// same extension point.
func add[T any](r *Registry, table map[string]T, name string, impl T, point pluginv1.ExtensionPoint, fill func(*sdkplugin.Clients)) error {
	if _, dup := table[name]; dup {
		return fmt.Errorf("plugin: built-in %s registered twice for %s", name, point)
	}
	table[name] = impl
	entry := r.builtins[name]
	if entry == nil {
		entry = &builtin{manifest: &pluginv1.Manifest{Name: name, Version: "builtin", ProtocolMajor: sdkplugin.ProtocolMajor}}
		r.builtins[name] = entry
	}
	entry.manifest.ExtensionPoints = append(entry.manifest.ExtensionPoints, point)
	fill(&entry.clients)
	return nil
}

// AddCodec registers a built-in config language and its file extensions.
func (r *Registry) AddCodec(name string, impl pluginv1.ConfigCodecService, extensions ...string) error {
	err := add(r, r.codecs, name, impl, pluginv1.ExtensionPoint_EXTENSION_POINT_CONFIG_CODEC, func(c *sdkplugin.Clients) { c.ConfigCodec = impl })
	if err != nil {
		return err
	}
	for _, extension := range extensions {
		r.extensions[extension] = name
	}
	r.builtins[name].manifest.FileExtensions = extensions
	return nil
}

// AddDetector registers a built-in resource detector.
func (r *Registry) AddDetector(name string, impl pluginv1.ResourceDetectorService) error {
	return add(r, r.detectors, name, impl, pluginv1.ExtensionPoint_EXTENSION_POINT_RESOURCE_DETECTOR, func(c *sdkplugin.Clients) { c.ResourceDetector = impl })
}

// AddBackend registers a built-in landing backend.
func (r *Registry) AddBackend(name string, impl pluginv1.LandingBackendService) error {
	return add(r, r.backends, name, impl, pluginv1.ExtensionPoint_EXTENSION_POINT_LANDING_BACKEND, func(c *sdkplugin.Clients) { c.LandingBackend = impl })
}

// AddGenerator registers a built-in message generator.
func (r *Registry) AddGenerator(name string, impl pluginv1.MessageGeneratorService) error {
	return add(r, r.generators, name, impl, pluginv1.ExtensionPoint_EXTENSION_POINT_MESSAGE_GENERATOR, func(c *sdkplugin.Clients) { c.MessageGenerator = impl })
}

// AddColumn registers a built-in list column.
func (r *Registry) AddColumn(name string, impl pluginv1.ListColumnService) error {
	return add(r, r.columns, name, impl, pluginv1.ExtensionPoint_EXTENSION_POINT_LIST_COLUMN, func(c *sdkplugin.Clients) { c.ListColumn = impl })
}

// addProcess puts a started plugin behind the same interfaces. A plugin named
// like a built-in replaces it.
func (r *Registry) addProcess(p *sdkplugin.Process) {
	r.processes = append(r.processes, p)
	name := p.Manifest.GetName()
	delete(r.builtins, name)
	for _, point := range p.Manifest.GetExtensionPoints() {
		switch point {
		case pluginv1.ExtensionPoint_EXTENSION_POINT_CONFIG_CODEC:
			r.codecs[name] = p.ConfigCodec
			for _, extension := range p.Manifest.GetFileExtensions() {
				r.extensions[extension] = name
			}
		case pluginv1.ExtensionPoint_EXTENSION_POINT_RESOURCE_DETECTOR:
			r.detectors[name] = p.ResourceDetector
		case pluginv1.ExtensionPoint_EXTENSION_POINT_LANDING_BACKEND:
			r.backends[name] = p.LandingBackend
		case pluginv1.ExtensionPoint_EXTENSION_POINT_MESSAGE_GENERATOR:
			r.generators[name] = p.MessageGenerator
		case pluginv1.ExtensionPoint_EXTENSION_POINT_LIST_COLUMN:
			r.columns[name] = p.ListColumn
		}
	}
}

// Skip records a plugin that cannot be used.
func (r *Registry) Skip(name, reason string) {
	r.skipped = append(r.skipped, Skipped{Name: name, Reason: reason})
}

// Skipped lists the plugins that could not be used.
func (r *Registry) Skipped() []Skipped { return slices.Clone(r.skipped) }

// Codec returns a config language by name, or nil.
func (r *Registry) Codec(name string) pluginv1.ConfigCodecService { return r.codecs[name] }

// CodecFor returns the config language of a file extension.
func (r *Registry) CodecFor(extension string) (pluginv1.ConfigCodecService, bool) {
	name, ok := r.extensions[extension]
	if !ok {
		return nil, false
	}
	return r.codecs[name], true
}

// Detector returns a resource detector by name, or nil.
func (r *Registry) Detector(name string) pluginv1.ResourceDetectorService { return r.detectors[name] }

// Backend returns a landing backend by name, or nil.
func (r *Registry) Backend(name string) pluginv1.LandingBackendService { return r.backends[name] }

// Generator returns a message generator by name, or nil.
func (r *Registry) Generator(name string) pluginv1.MessageGeneratorService { return r.generators[name] }

// Column returns a list column by name, or nil.
func (r *Registry) Column(name string) pluginv1.ListColumnService { return r.columns[name] }

// Builtins lists the built-ins' names, sorted.
func (r *Registry) Builtins() []string {
	names := make([]string, 0, len(r.builtins))
	for name := range r.builtins {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Builtin returns a built-in's manifest and clients.
func (r *Registry) Builtin(name string) (*pluginv1.Manifest, sdkplugin.Clients, bool) {
	entry, ok := r.builtins[name]
	if !ok {
		return nil, sdkplugin.Clients{}, false
	}
	return entry.manifest, entry.clients, true
}

// Close stops every started plugin.
func (r *Registry) Close() {
	for _, process := range r.processes {
		_ = process.Close()
	}
}
