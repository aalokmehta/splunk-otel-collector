// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//       http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package configprovider

import (
	"context"
	"errors"
	"os"
	"path"
	"testing"

	"github.com/knadh/koanf/maps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/confmaptest"
	"go.uber.org/zap"
)

var errValueUpdated = errors.New("configuration must retrieve the updated value")

func TestConfigSourceManagerNewManager(t *testing.T) {
	tests := []struct {
		factories Factories
		wantErr   string
		name      string
		file      string
	}{
		{
			name: "basic_config",
			file: "basic_config",
			factories: Factories{
				"tstcfgsrc": &mockCfgSrcFactory{},
			},
		},
		{
			name:      "unknown_type",
			file:      "basic_config",
			factories: Factories{},
			wantErr:   "unknown config_sources type \"tstcfgsrc\"",
		},
		{
			name: "build_error",
			file: "basic_config",
			factories: Factories{
				"tstcfgsrc": &mockCfgSrcFactory{
					ErrOnCreateConfigSource: errors.New("forced test error"),
				},
			},
			wantErr: "failed to create config source tstcfgsrc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := path.Join("testdata", tt.file+".yaml")
			parser, err := confmaptest.LoadConf(filename)
			require.NoError(t, err)

			_, _, err = Resolve(context.Background(), parser, zap.NewNop(), component.NewDefaultBuildInfo(), tt.factories, nil)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConfigSourceManagerSimple(t *testing.T) {
	cfgSources := map[string]ConfigSource{
		"tstcfgsrc": &testConfigSource{
			ValueMap: map[string]valueEntry{
				"test_selector": {Value: "test_value"},
			},
		},
	}

	originalCfg := map[string]any{
		"top0": map[string]any{
			"int":    1,
			"cfgsrc": "$tstcfgsrc:test_selector",
		},
	}
	expectedCfg := map[string]any{
		"top0": map[string]any{
			"int":    1,
			"cfgsrc": "test_value",
		},
	}

	cp := confmap.NewFromStringMap(originalCfg)

	res, closeFunc, err := resolve(context.Background(), cfgSources, cp, func(event *confmap.ChangeEvent) {
		panic("must not be called")
	})
	require.NoError(t, err)
	assert.Equal(t, expectedCfg, maps.Unflatten(res, confmap.KeyDelimiter))
	assert.NoError(t, closeFunc(context.Background()))
}

func TestConfigSourceManagerResolveRemoveConfigSourceSection(t *testing.T) {
	cfg := map[string]any{
		"config_sources": map[string]any{
			"testcfgsrc": nil,
		},
		"another_section": map[string]any{
			"int": 42,
		},
	}

	cfgSources := map[string]ConfigSource{
		"tstcfgsrc": &testConfigSource{},
	}

	res, closeFunc, err := resolve(context.Background(), cfgSources, confmap.NewFromStringMap(cfg), func(event *confmap.ChangeEvent) {
		panic("must not be called")
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	delete(cfg, "config_sources")
	assert.Equal(t, cfg, maps.Unflatten(res, confmap.KeyDelimiter))
	assert.NoError(t, callClose(closeFunc))
}

func TestConfigSourceManagerResolveErrors(t *testing.T) {
	testErr := errors.New("test error")

	tests := []struct {
		config          map[string]any
		configSourceMap map[string]ConfigSource
		name            string
	}{
		{
			name: "incorrect_cfgsrc_ref",
			config: map[string]any{
				"cfgsrc": "$tstcfgsrc:selector?{invalid}",
			},
			configSourceMap: map[string]ConfigSource{
				"tstcfgsrc": &testConfigSource{},
			},
		},
		{
			name: "error_on_retrieve",
			config: map[string]any{
				"cfgsrc": "$tstcfgsrc:selector",
			},
			configSourceMap: map[string]ConfigSource{
				"tstcfgsrc": &testConfigSource{ErrOnRetrieve: testErr},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, closeFunc, err := resolve(context.Background(), tt.configSourceMap, confmap.NewFromStringMap(tt.config), func(event *confmap.ChangeEvent) {
				panic("must not be called")
			})
			require.Error(t, err)
			require.Nil(t, res)
			assert.NoError(t, callClose(closeFunc))
		})
	}
}

func TestConfigSourceManagerYAMLInjection(t *testing.T) {
	cfgSources := map[string]ConfigSource{
		"tstcfgsrc": &testConfigSource{
			ValueMap: map[string]valueEntry{
				"valid_yaml_str": {Value: `
bool: true
int: 42
source: string
map:
  k0: v0
  k1: v1
`},
				"invalid_yaml_str": {Value: ":"},
			},
		},
	}

	file := path.Join("testdata", "yaml_injection.yaml")
	cp, err := confmaptest.LoadConf(file)
	require.NoError(t, err)

	expectedFile := path.Join("testdata", "yaml_injection_expected.yaml")
	expectedParser, err := confmaptest.LoadConf(expectedFile)
	require.NoError(t, err)
	expectedCfg := expectedParser.ToStringMap()

	res, closeFunc, err := resolve(context.Background(), cfgSources, cp, func(event *confmap.ChangeEvent) {
		panic("must not be called")
	})
	require.NoError(t, err)
	assert.Equal(t, expectedCfg, maps.Unflatten(res, confmap.KeyDelimiter))
	assert.NoError(t, callClose(closeFunc))
}

func TestConfigSourceManagerArraysAndMaps(t *testing.T) {
	cfgSources := map[string]ConfigSource{
		"tstcfgsrc": &testConfigSource{
			ValueMap: map[string]valueEntry{
				"elem0": {Value: "elem0_value"},
				"elem1": {Value: "elem1_value"},
				"k0":    {Value: "k0_value"},
				"k1":    {Value: "k1_value"},
			},
		},
	}

	file := path.Join("testdata", "arrays_and_maps.yaml")
	cp, err := confmaptest.LoadConf(file)
	require.NoError(t, err)

	expectedFile := path.Join("testdata", "arrays_and_maps_expected.yaml")
	expectedParser, err := confmaptest.LoadConf(expectedFile)
	require.NoError(t, err)

	res, closeFunc, err := resolve(context.Background(), cfgSources, cp, func(event *confmap.ChangeEvent) {
		panic("must not be called")
	})
	require.NoError(t, err)
	assert.Equal(t, expectedParser.ToStringMap(), maps.Unflatten(res, confmap.KeyDelimiter))
	assert.NoError(t, callClose(closeFunc))
}

func TestConfigSourceManagerParamsHandling(t *testing.T) {
	tstCfgSrc := testConfigSource{
		ValueMap: map[string]valueEntry{
			"elem0": {Value: nil},
			"elem1": {
				Value: map[string]any{
					"p0": true,
					"p1": "a string with spaces",
					"p3": 42,
				},
			},
			"k0": {Value: nil},
			"k1": {
				Value: map[string]any{
					"p0": true,
					"p1": "a string with spaces",
					"p2": map[string]any{
						"p2_0": "a nested map0",
						"p2_1": true,
					},
				},
			},
		},
	}

	// Set OnRetrieve to check if the parameters were parsed as expected.
	tstCfgSrc.OnRetrieve = func(ctx context.Context, selector string, paramsConfigMap *confmap.Conf) error {
		var val any
		if paramsConfigMap != nil {
			val = paramsConfigMap.ToStringMap()
		}
		assert.Equal(t, tstCfgSrc.ValueMap[selector].Value, val)
		return nil
	}

	file := path.Join("testdata", "params_handling.yaml")
	cp, err := confmaptest.LoadConf(file)
	require.NoError(t, err)

	expectedFile := path.Join("testdata", "params_handling_expected.yaml")
	expectedParser, err := confmaptest.LoadConf(expectedFile)
	require.NoError(t, err)

	res, closeFunc, err := resolve(context.Background(), map[string]ConfigSource{"tstcfgsrc": &tstCfgSrc}, cp, func(event *confmap.ChangeEvent) {
		panic("must not be called")
	})
	require.NoError(t, err)
	assert.Equal(t, expectedParser.ToStringMap(), maps.Unflatten(res, confmap.KeyDelimiter))
	assert.NoError(t, callClose(closeFunc))
}

func TestConfigSourceManagerWatchForUpdate(t *testing.T) {
	watchForUpdateCh := make(chan error, 1)

	cfgSources := map[string]ConfigSource{
		"tstcfgsrc": &testConfigSource{
			ValueMap: map[string]valueEntry{
				"test_selector": {
					Value:            "test_value",
					WatchForUpdateCh: watchForUpdateCh,
				},
			},
		},
	}

	originalCfg := map[string]any{
		"top0": map[string]any{
			"var0": "$tstcfgsrc:test_selector",
		},
	}

	cp := confmap.NewFromStringMap(originalCfg)
	watchCh := make(chan *confmap.ChangeEvent)
	_, closeFunc, err := resolve(context.Background(), cfgSources, cp, func(event *confmap.ChangeEvent) {
		watchCh <- event
	})
	require.NoError(t, err)

	watchForUpdateCh <- nil

	ce := <-watchCh
	assert.NoError(t, ce.Error)
	assert.NoError(t, callClose(closeFunc))
}

func TestConfigSourceManagerMultipleWatchForUpdate(t *testing.T) {
	watchForUpdateCh := make(chan error, 2)
	cfgSources := map[string]ConfigSource{
		"tstcfgsrc": &testConfigSource{
			ValueMap: map[string]valueEntry{
				"test_selector": {
					Value:            "test_value",
					WatchForUpdateCh: watchForUpdateCh,
				},
			},
		},
	}

	originalCfg := map[string]any{
		"top0": map[string]any{
			"var0": "$tstcfgsrc:test_selector",
			"var1": "$tstcfgsrc:test_selector",
			"var2": "$tstcfgsrc:test_selector",
			"var3": "$tstcfgsrc:test_selector",
		},
	}

	cp := confmap.NewFromStringMap(originalCfg)
	watchCh := make(chan *confmap.ChangeEvent)
	_, closeFunc, err := resolve(context.Background(), cfgSources, cp, func(event *confmap.ChangeEvent) {
		watchCh <- event
	})
	require.NoError(t, err)

	watchForUpdateCh <- errValueUpdated
	watchForUpdateCh <- errValueUpdated

	ce := <-watchCh
	assert.ErrorIs(t, ce.Error, errValueUpdated)
	close(watchForUpdateCh)
	assert.NoError(t, callClose(closeFunc))
}

func TestConfigSourceManagerEnvVarHandling(t *testing.T) {
	require.NoError(t, os.Setenv("envvar", "envvar_value"))
	defer func() {
		assert.NoError(t, os.Unsetenv("envvar"))
	}()

	tstCfgSrc := testConfigSource{
		ValueMap: map[string]valueEntry{
			"int_key": {Value: 42},
		},
	}

	// Intercept "params_key" and create an entry with the params themselves.
	tstCfgSrc.OnRetrieve = func(ctx context.Context, selector string, paramsConfigMap *confmap.Conf) error {
		var val any
		if paramsConfigMap != nil {
			val = paramsConfigMap.ToStringMap()
		}
		if selector == "params_key" {
			tstCfgSrc.ValueMap[selector] = valueEntry{Value: val}
		}
		return nil
	}

	file := path.Join("testdata", "envvar_cfgsrc_mix.yaml")
	cp, err := confmaptest.LoadConf(file)
	require.NoError(t, err)

	expectedFile := path.Join("testdata", "envvar_cfgsrc_mix_expected.yaml")
	expectedParser, err := confmaptest.LoadConf(expectedFile)
	require.NoError(t, err)

	res, closeFunc, err := resolve(context.Background(), map[string]ConfigSource{"tstcfgsrc": &tstCfgSrc}, cp, func(event *confmap.ChangeEvent) {
		panic("must not be called")
	})
	require.NoError(t, err)
	assert.Equal(t, expectedParser.ToStringMap(), res)
	assert.NoError(t, callClose(closeFunc))
}

func TestManagerExpandString(t *testing.T) {
	ctx := context.Background()
	cfgSources := map[string]ConfigSource{
		"tstcfgsrc": &testConfigSource{
			ValueMap: map[string]valueEntry{
				"str_key": {Value: "test_value"},
				"int_key": {Value: 1},
				"nil_key": {Value: nil},
			},
		},
		"tstcfgsrc/named": &testConfigSource{
			ValueMap: map[string]valueEntry{
				"int_key": {Value: 42},
			},
		},
	}

	require.NoError(t, os.Setenv("envvar", "envvar_value"))
	defer func() {
		assert.NoError(t, os.Unsetenv("envvar"))
	}()
	require.NoError(t, os.Setenv("envvar_str_key", "str_key"))
	defer func() {
		assert.NoError(t, os.Unsetenv("envvar_str_key"))
	}()

	tests := []struct {
		want    any
		wantErr error
		name    string
		input   string
	}{
		{
			name:  "literal_string",
			input: "literal_string",
			want:  "literal_string",
		},
		{
			name:  "escaped_$",
			input: "$$tstcfgsrc:int_key$$envvar",
			want:  "$tstcfgsrc:int_key$envvar",
		},
		{
			name:  "cfgsrc_int",
			input: "$tstcfgsrc:int_key",
			want:  1,
		},
		{
			name:  "concatenate_cfgsrc_string",
			input: "prefix-$tstcfgsrc:str_key",
			want:  "prefix-test_value",
		},
		{
			name:  "concatenate_cfgsrc_non_string",
			input: "prefix-$tstcfgsrc:int_key",
			want:  "prefix-1",
		},
		{
			name:  "envvar",
			input: "$envvar",
			want:  "envvar_value",
		},
		{
			name:  "prefixed_envvar",
			input: "prefix-$envvar",
			want:  "prefix-envvar_value",
		},
		{
			name:    "envvar_treated_as_cfgsrc",
			input:   "$envvar:suffix",
			wantErr: &errUnknownConfigSource{},
		},
		{
			name:  "cfgsrc_using_envvar",
			input: "$tstcfgsrc:$envvar_str_key",
			want:  "test_value",
		},
		{
			name:  "envvar_cfgsrc_using_envvar",
			input: "$envvar/$tstcfgsrc:$envvar_str_key",
			want:  "envvar_value/test_value",
		},
		{
			name:  "delimited_cfgsrc",
			input: "${tstcfgsrc:int_key}",
			want:  1,
		},
		{
			name:    "unknown_delimited_cfgsrc",
			input:   "${cfgsrc:int_key}",
			wantErr: &errUnknownConfigSource{},
		},
		{
			name:  "delimited_cfgsrc_with_spaces",
			input: "${ tstcfgsrc: int_key }",
			want:  1,
		},
		{
			name:  "interpolated_and_delimited_cfgsrc",
			input: "0/${ tstcfgsrc: $envvar_str_key }/2/${tstcfgsrc:int_key}",
			want:  "0/test_value/2/1",
		},
		{
			name:  "named_config_src",
			input: "$tstcfgsrc/named:int_key",
			want:  42,
		},
		{
			name:  "named_config_src_bracketed",
			input: "${tstcfgsrc/named:int_key}",
			want:  42,
		},
		{
			name:  "envvar_name_separator",
			input: "$envvar/test/test",
			want:  "envvar_value/test/test",
		},
		{
			name:    "envvar_treated_as_cfgsrc",
			input:   "$envvar/test:test",
			wantErr: &errUnknownConfigSource{},
		},
		{
			name:  "retrieved_nil",
			input: "${tstcfgsrc:nil_key}",
		},
		{
			name:  "retrieved_nil_on_string",
			input: "prefix-${tstcfgsrc:nil_key}-suffix",
			want:  "prefix--suffix",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, closeFunc, err := parseStringValue(ctx, cfgSources, tt.input, func(event *confmap.ChangeEvent) {
				panic("must not be called")
			})
			if tt.wantErr != nil {
				require.Error(t, err)
				require.IsType(t, tt.wantErr, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, callClose(closeFunc))
			require.Equal(t, tt.want, got)
		})
	}
}

func Test_parseCfgSrc(t *testing.T) {
	tests := []struct {
		params     any
		name       string
		str        string
		cfgSrcName string
		selector   string
		wantErr    bool
	}{
		{
			name:       "basic",
			str:        "cfgsrc:selector",
			cfgSrcName: "cfgsrc",
			selector:   "selector",
		},
		{
			name:    "missing_selector",
			str:     "cfgsrc",
			wantErr: true,
		},
		{
			name:       "params",
			str:        "cfgsrc:selector?p0=1&p1=a_string&p2=true",
			cfgSrcName: "cfgsrc",
			selector:   "selector",
			params: map[string]any{
				"p0": 1,
				"p1": "a_string",
				"p2": true,
			},
		},
		{
			name:       "query_pass_nil",
			str:        "cfgsrc:selector?p0&p1&p2",
			cfgSrcName: "cfgsrc",
			selector:   "selector",
			params: map[string]any{
				"p0": nil,
				"p1": nil,
				"p2": nil,
			},
		},
		{
			name:       "array_in_params",
			str:        "cfgsrc:selector?p0=0&p0=1&p0=2&p1=done",
			cfgSrcName: "cfgsrc",
			selector:   "selector",
			params: map[string]any{
				"p0": []any{0, 1, 2},
				"p1": "done",
			},
		},
		{
			name:       "empty_param",
			str:        "cfgsrc:selector?no_closing=",
			cfgSrcName: "cfgsrc",
			selector:   "selector",
			params: map[string]any{
				"no_closing": any(nil),
			},
		},
		{
			name:       "use_url_encode",
			str:        "cfgsrc:selector?p0=contains+%3D+and+%26+too",
			cfgSrcName: "cfgsrc",
			selector:   "selector",
			params: map[string]any{
				"p0": "contains = and & too",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgSrcName, selector, paramsConfigMap, err := parseCfgSrcInvocation(tt.str)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.cfgSrcName, cfgSrcName)
			assert.Equal(t, tt.selector, selector)
			var val any
			if paramsConfigMap != nil {
				val = paramsConfigMap.ToStringMap()
			}
			assert.Equal(t, tt.params, val)
		})
	}
}

func callClose(closeFunc confmap.CloseFunc) error {
	if closeFunc == nil {
		return nil
	}
	return closeFunc(context.Background())
}
