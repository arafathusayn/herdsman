package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/arafathusayn/herdsman/sdk/client"
	suggestv1 "github.com/arafathusayn/herdsman/sdk/suggest/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// dialSuggest reaches the daemon through the SDK; tests replace it.
var dialSuggest = func() (suggestv1.SuggestService, func(), error) {
	sdk, err := client.Dial(client.Options{})
	if err != nil {
		return nil, nil, err
	}
	return sdk.Suggest, sdk.Close, nil
}

// invokeSuggest carries one verb's JSON over the typed client. The field names
// are the contract's own, so --json prints what the contract says.
func invokeSuggest[Req, Res proto.Message](ctx context.Context, request []byte, message Req, call func(context.Context, Req) (Res, error)) ([]byte, error) {
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(request, message); err != nil {
		return nil, err
	}
	response, err := call(ctx, message)
	if err != nil {
		return nil, rpcText(err)
	}
	data, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(response)
	if err != nil {
		return nil, err
	}
	// protojson varies its spacing on purpose; printed output should not.
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

// sdkSuggestCall serves one catalog verb from the daemon.
func sdkSuggestCall(ctx context.Context, method string, request []byte) ([]byte, error) {
	service, closeService, err := dialSuggest()
	if err != nil {
		return nil, err
	}
	defer closeService()
	switch method {
	case "Search":
		return invokeSuggest(ctx, request, &suggestv1.SearchRequest{}, service.Search)
	case "Get":
		return invokeSuggest(ctx, request, &suggestv1.GetRequest{}, service.Get)
	case "Add":
		return invokeSuggest(ctx, request, &suggestv1.AddRequest{}, service.Add)
	case "Plan":
		return invokeSuggest(ctx, request, &suggestv1.PlanRequest{}, service.Plan)
	case "Sources":
		return invokeSuggest(ctx, request, &suggestv1.SourcesRequest{}, service.Sources)
	case "SourceAdd":
		return invokeSuggest(ctx, request, &suggestv1.SourceAddRequest{}, service.SourceAdd)
	case "SourceRemove":
		return invokeSuggest(ctx, request, &suggestv1.SourceRemoveRequest{}, service.SourceRemove)
	case "SourcePreview":
		return invokeSuggest(ctx, request, &suggestv1.SourcePreviewRequest{}, service.SourcePreview)
	case "SourcePin":
		return invokeSuggest(ctx, request, &suggestv1.SourcePinRequest{}, service.SourcePin)
	}
	return nil, fmt.Errorf("suggest: no method %q", method)
}
