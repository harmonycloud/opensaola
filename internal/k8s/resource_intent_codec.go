/*
Copyright 2025 The OpenSaola Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package k8s

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/klauspost/compress/zstd"
)

const intentJSONKey = "intent.json"
const intentZstdKey = "intent.json.zst"

// EncodeAll and DecodeAll support concurrent callers. A single worker per codec
// bounds internal concurrency without recreating expensive state for each record.
var intentEncoder = sync.OnceValues(func() (*zstd.Encoder, error) {
	return zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
})
var intentDecoder = sync.OnceValues(func() (*zstd.Decoder, error) {
	return zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
})

func encodeResourceIntent(record resourceIntent) (map[string][]byte, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, errors.New("cannot encode resource intent")
	}
	encoder, err := intentEncoder()
	if err != nil {
		return nil, errors.New("cannot initialize resource intent encoder")
	}
	compressed := encoder.EncodeAll(raw, nil)
	if len(compressed) < len(raw) {
		return map[string][]byte{intentZstdKey: compressed}, nil
	}
	return map[string][]byte{intentJSONKey: raw}, nil
}

func decodeResourceIntent(data map[string][]byte) (*resourceIntent, error) {
	raw, plain := data[intentJSONKey]
	compressed, packed := data[intentZstdKey]
	if plain == packed {
		return nil, errors.New("missing or ambiguous resource intent encoding")
	}
	if packed {
		decoder, err := intentDecoder()
		if err != nil {
			return nil, errors.New("cannot initialize resource intent decoder")
		}
		raw, err = decoder.DecodeAll(compressed, nil)
		if err != nil {
			// Do not expose decoder or JSON errors that might contain credentials.
			return nil, errors.New("invalid compressed resource intent")
		}
	}
	var record resourceIntent
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, errors.New("invalid resource intent JSON")
	}
	return &record, nil
}
