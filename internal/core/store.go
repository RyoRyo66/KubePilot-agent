package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Store interface {
	Load() (State, error)
	Save(State) error
}

type FileStore struct{ Path string }

func (f FileStore) Load() (State, error) {
	var state State
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(b, &state)
	if err == nil && state.Schema != 1 {
		err = fmt.Errorf("unsupported state schema %d", state.Schema)
	}
	return state, err
}

func (f FileStore) Save(state State) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(f.Path), ".state-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(b)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), f.Path)
}

func clone(state State) State {
	b, _ := json.Marshal(state)
	var copy State
	_ = json.Unmarshal(b, &copy)
	return copy
}
