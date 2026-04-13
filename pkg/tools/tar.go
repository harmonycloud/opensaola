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

package tools

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"strings"
)

type TarInfo struct {
	Name  string            `json:"file_name"`
	Files map[string][]byte `json:"file_data"`
}

func (t *TarInfo) ReadFile(name string) ([]byte, error) {
	for k, v := range t.Files {
		if strings.Contains(k, name) {
			return v, nil
		}
	}
	return nil, errors.New("file not found")
}

func ReadTarInfo(data []byte) (*TarInfo, error) {
	info := new(TarInfo)
	info.Files = make(map[string][]byte)
	tr := tar.NewReader(bytes.NewBuffer(data))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break // end of archive
		} else if err != nil {
			return nil, err
		}

		if hdr.Typeflag == tar.TypeReg {
			dirs := strings.Split(hdr.Name, "/")
			name := strings.Join(dirs[1:], "/")

			info.Files[name], err = io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
		}
	}
	return info, nil
}
