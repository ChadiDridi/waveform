package waveplan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

// Load reads a WavePlan from a YAML or JSON file.
func Load(path string) (*WavePlan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wp WavePlan
	if err := yaml.UnmarshalStrict(b, &wp); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if wp.Kind != Kind || wp.APIVersion != APIVersion {
		return nil, fmt.Errorf("%s: want kind %s, apiVersion %s", path, Kind, APIVersion)
	}
	return &wp, nil
}

// YAML renders the WavePlan.
func (wp *WavePlan) YAML() ([]byte, error) { return yaml.Marshal(wp) }

// LoadStatus reads a status file. A missing file is an empty status.
func LoadStatus(path string) (Status, error) {
	var st Status
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(b, &st)
	return st, err
}

// SaveStatus writes a status file atomically.
func SaveStatus(path string, st Status) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".waveform-state-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
