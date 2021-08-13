package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func main() {
	arch := GetSystemInfo()
	installer_dir := SetupTemporaryDirectory(arch)
	installer_path := DownloadInstaller(installer_dir, arch)
	RunInstaller(installer_path)
	Cleanup(installer_dir)
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

func SetupTemporaryDirectory(arch string) (installer_dir string) {
	installer_dir, err := ioutil.TempDir("", "vscode-winsta11er")
	checkError(err)

	return installer_dir
}

func DownloadInstaller(installer_dir, arch string) (installer_path string) {
	var downloadUrl = strings.Replace("https://update.code.visualstudio.com/latest/win32-$arch-user/stable", "$arch", arch, 1)

	fmt.Printf("Downloading installer from %s.\n", downloadUrl)

	file, err := os.CreateTemp(installer_dir, strings.Replace("vscode-win32-$arch-user*.exe", "$arch", arch, 1))
	checkError(err)

	client := http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			r.URL.Opaque = r.URL.Path
			return nil
		},
	}

	// Put content on file
	resp, err := client.Get(downloadUrl)
	checkError(err)
	defer resp.Body.Close()

	_, err = io.Copy(file, resp.Body)
	checkError(err)
	defer file.Close()

	fmt.Printf("Downloaded installer to file %s.\n", file.Name())
	installer_path = file.Name()

	return installer_path
}

func RunInstaller(installer_path string) {
	path, err := exec.LookPath(installer_path)
	checkError(err)

	cmd := exec.Command(path, "/verysilent", "/mergetasks=!runcode")
	stdout, err := cmd.Output()
	checkError(err)

	fmt.Println(string(stdout))
}

func Cleanup(installer_dir string) {
	err := os.RemoveAll(installer_dir)
	checkError(err)
}

func checkError(e error) {
	if e != nil {
		panic(e)
	}
}
