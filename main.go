package main

import (
	"context"
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

func main() {
	arch := GetSystemInfo()
	installer_dir, err := SetupTemporaryDirectory(arch)
	checkError(err)
	installer_path, err := DownloadInstaller(installer_dir, arch)
	checkError(err)
	err = RunInstaller(installer_path)
	checkError(err)
	err = Cleanup(installer_dir)
	checkError(err)
}

func GetSystemInfo() (arch string) {
	switch runtime.GOARCH {
	case "amd64":
		arch = "x64"
	case "386":
		arch = "ia32"
	case "arm64":
		arch = "arm64"
	}

	return
}

func SetupTemporaryDirectory(arch string) (installer_dir string, err error) {
	return ioutil.TempDir("", "vscode-winsta11er")
}

func DownloadInstaller(installer_dir, arch string) (installer_path string, err error) {
	downloadUrl := fmt.Sprintf("https://update.code.visualstudio.com/latest/win32-%s-user/stable", arch)

	fmt.Printf("Downloading installer from %s.\n", downloadUrl)

	file, err := os.CreateTemp(installer_dir, fmt.Sprintf("vscode-win32-%s-user*.exe", arch))
	if err != nil {
		return "", err
	}
	defer file.Close()

	client := http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			r.URL.Opaque = r.URL.Path
			return nil
		},
		Transport: &http.Transport{
			Dial: (&net.Dialer{
				Timeout: 30 * time.Second,
			}).Dial,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		},
	}

	// Put content on file
	resp, err := client.Get(downloadUrl)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", errors.New("invalid response status code")
	}

	written, err := copyWithTimeout(context.Background(), file, resp.Body)
	if err != nil {
		return "", err
	}
	fmt.Println(written)

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

func copyWithTimeout(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	// Every 5 seconds, ensure at least 200 bytes (40 bytes/second average) are read
	interval := 5
	minCopyBytes := int64(200)
	prevWritten := int64(0)
	written := int64(0)

	done := make(chan error)
	mu := sync.Mutex{}
	t := time.NewTicker(time.Duration(interval) * time.Second)
	defer t.Stop()

	// Read the stream, 32KB at a time
	go func() {
		buf := make([]byte, 32<<10)
		for {
			readBytes, readErr := src.Read(buf)
			if readBytes > 0 {
				writeBytes, writeErr := dst.Write(buf[0:readBytes])
				mu.Lock()
				written += int64(writeBytes)
				mu.Unlock()
				if writeErr != nil {
					done <- writeErr
					return
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					done <- readErr
				} else {
					done <- nil
				}
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
