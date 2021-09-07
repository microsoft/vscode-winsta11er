/*---------------------------------------------------------------------------------------------
 *  Copyright (c) Microsoft Corporation. All rights reserved.
 *  Licensed under the MIT License. See License.txt in the project root for license information.
 *--------------------------------------------------------------------------------------------*/

package common

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

func MainBase(quality string) {
	arch_pkg := GetSystemInfo()
	installer_dir, err := SetupTemporaryDirectory(quality)
	checkError(err)
	release_info, err := GetReleaseInfo(arch_pkg, quality)
	checkError(err)
	installer_path, err := DownloadInstaller(installer_dir, arch_pkg, release_info)
	checkError(err)
	err = RunInstaller(installer_path)
	checkError(err)
	err = Cleanup(installer_dir)
	checkError(err)
}

func GetSystemInfo() (arch_pkg string) {
	switch runtime.GOARCH {
	case "amd64":
		arch_pkg = "x64-user"
	case "386":
		arch_pkg = "user"
	case "arm64":
		arch_pkg = "arm64-user"
	}

	return
}

func SetupTemporaryDirectory(quality string) (installer_dir string, err error) {
	return ioutil.TempDir("", fmt.Sprintf("vscode-winsta11er-%s", quality))
}

type ReleaseInfo struct {
	Url        string `json:"url"`
	Name       string `json:"name"`
	Sha256Hash string `json:"sha256hash"`
}

func GetReleaseInfo(arch_pkg string, quality string) (info *ReleaseInfo, err error) {
	apiUrl := fmt.Sprintf("https://update.code.visualstudio.com/api/update/win32-%s/%s/latest", arch_pkg, quality)
	fmt.Printf("Requesting hash from %s.\n", apiUrl)
	client := http.Client{
		Timeout: 30 * time.Second,
	}
	req, err := http.NewRequest("GET", apiUrl, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "cli/vscode-winsta11er")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, errors.New("invalid response status code")
	}
	info = &ReleaseInfo{}
	err = json.NewDecoder(resp.Body).Decode(info)
	if err != nil {
		return nil, err
	}
	if info.Url == "" || info.Name == "" || info.Sha256Hash == "" {
		return nil, errors.New("missing required fields in API response")
	}
	return info, nil
}

func DownloadInstaller(installer_dir, arch_pkg string, info *ReleaseInfo) (installer_path string, err error) {
	fmt.Printf("Downloading installer from %s.\n", info.Url)

	file, err := os.CreateTemp(installer_dir, fmt.Sprintf("vscode-win32-%s*.exe", arch_pkg))
	if err != nil {
		return "", err
	}
	defer file.Close()

	client := http.Client{
		// Disable timeout here because file can take a while to download on slow connections
		// Instead, we're using a function that reads from the stream and makes sure data is flowing constantly
		Timeout: 0,
		Transport: &http.Transport{
			Dial: (&net.Dialer{
				Timeout: 30 * time.Second,
			}).Dial,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		},
	}

	// Request the file
	req, err := http.NewRequest("GET", info.Url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "cli/vscode-winsta11er")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", errors.New("invalid response status code")
	}

	// Copy the stream to the file and calculate the hash
	_, err = copyWithTimeout(context.Background(), file, resp.Body, info.Sha256Hash)
	if err != nil {
		return "", err
	}

	fmt.Printf("Downloaded installer to file %s.\n", file.Name())
	installer_path = file.Name()

	return installer_path, nil
}

func RunInstaller(installer_path string) error {
	path, err := exec.LookPath(installer_path)
	if err != nil {
		return err
	}

	cmd := exec.Command(path, "/verysilent", "/mergetasks=!runcode")
	stdout, err := cmd.Output()
	if err != nil {
		return err
	}

	fmt.Println(string(stdout))
	return nil
}

func Cleanup(installer_dir string) error {
	return os.RemoveAll(installer_dir)
}

func checkError(e error) {
	if e != nil {
		panic(e)
	}
}

func copyWithTimeout(ctx context.Context, dst io.Writer, src io.Reader, expectedHash string) (int64, error) {
	// Every 5 seconds, ensure at least 200 bytes (40 bytes/second average) are read
	interval := 5
	minCopyBytes := int64(200)
	prevWritten := int64(0)
	written := int64(0)

	expectedHashBytes, err := hex.DecodeString(expectedHash)
	if err != nil {
		return 0, fmt.Errorf("error decoding hash from hex: %v", err)
	}

	h := sha256.New()
	done := make(chan error)
	mu := sync.Mutex{}
	t := time.NewTicker(time.Duration(interval) * time.Second)
	defer t.Stop()

	// Read the stream, 32KB at a time
	go func() {
		var (
			writeErr, readErr, hashErr error
			writeBytes, readBytes      int
			buf                        = make([]byte, 32<<10)
		)
		for {
			readBytes, readErr = src.Read(buf)
			if readBytes > 0 {
				// Add to the hash
				_, hashErr = h.Write(buf[0:readBytes])
				if hashErr != nil {
					done <- hashErr
					return
				}

				// Write to disk and update the number of bytes written
				writeBytes, writeErr = dst.Write(buf[0:readBytes])
				mu.Lock()
				written += int64(writeBytes)
				mu.Unlock()
				if writeErr != nil {
					done <- writeErr
					return
				}
			}
			if readErr != nil {
				// If error is EOF, means we read the entire file, so don't consider that as error
				if readErr != io.EOF {
					done <- readErr
					return
				}

				// Compute and compare the checksum
				hash := h.Sum(nil)
				if !bytes.Equal(expectedHashBytes, hash[:]) {
					done <- errors.New("downloaded file's hash doesn't match")
					return
				}

				// No error
				done <- nil
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		case <-t.C:
			mu.Lock()
			if written < prevWritten+minCopyBytes {
				mu.Unlock()
				return written, fmt.Errorf("stream stalled: received %d bytes over the last %d seconds", written, interval)
			}
			prevWritten = written
			mu.Unlock()
		case e := <-done:
			return written, e
		}
	}
}
