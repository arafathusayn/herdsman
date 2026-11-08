package config

import (
	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
	"google.golang.org/protobuf/proto"
)

// decodeBin reads the binary form into a canonical tree, so that it goes
// through the same layers as a text file.
func decodeBin(data []byte) (*tree.Node, error) {
	msg := &configv1.Config{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, err
	}
	return messageTree(msg.ProtoReflect()), nil
}

// encodeBin writes the binary form. Deterministic output keeps a regenerated
// file from showing as changed.
func encodeBin(msg *configv1.Config) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(msg)
}
