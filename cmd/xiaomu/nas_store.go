package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arixbit/xiaomu-desktop/internal/nas"
)

// The saved NAS lives in nas.json beside source.json, in the app's user
// data directory. Only one NAS is kept, like the one saved source: the
// password is stored in that file, on this machine only, and never leaves
// it except to log in to the NAS itself.
func savedNASPath() (string, error) {
	sourcePath, err := savedSourcePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(sourcePath), "nas.json"), nil
}

func readSavedNAS(path string) (nas.Server, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nas.Server{}, false, nil
	}
	if err != nil {
		return nas.Server{}, false, err
	}
	var server nas.Server
	if err := json.Unmarshal(data, &server); err != nil {
		return nas.Server{}, false, fmt.Errorf("读取已保存内网盘: %w", err)
	}
	return server, true, nil
}

func writeSavedNAS(path string, server nas.Server) error {
	return writeJSONFile(path, server)
}

func (a *app) saveNAS(server nas.Server) error {
	path, err := savedNASPath()
	if err != nil {
		return err
	}
	return writeSavedNAS(path, server)
}

func (a *app) removeSavedNAS() error {
	path, err := savedNASPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// restoreNAS loads the saved NAS, if any, and fills the add form with it
// so reconnecting is one tap. It does not connect: the connection is
// made when the 内网盘 tab is opened or the user taps 重新连接.
func (a *app) restoreNAS() {
	path, err := savedNASPath()
	if err != nil {
		a.status = "读取本机配置目录失败：" + err.Error()
		return
	}
	server, ok, err := readSavedNAS(path)
	if err != nil {
		a.status = err.Error()
		return
	}
	if !ok {
		return
	}
	a.nas.server, a.nas.hasServer = server, true
	a.nas.fillForm(server)
}
