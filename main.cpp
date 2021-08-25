/*---------------------------------------------------------------------------------------------
 *  Copyright (c) Microsoft Corporation. All rights reserved.
 *  Licensed under the MIT License. See License.txt in the project root for license information.
 *--------------------------------------------------------------------------------------------*/

#include "main.h"

#include <atomic>
#include <cstdint>
#include <fcntl.h>
#include <filesystem>
#include <iostream>
#include <io.h>
#include <memory>
#include <string>
#include <vector>
#include <winrt\Windows.Security.Cryptography.h>
#include <winrt\Windows.Security.Cryptography.Core.h>
#include <winrt\Windows.Data.Json.h>
#include <winrt\Windows.Foundation.Collections.h>
#include <winrt\Windows.Storage.h>
#include <winrt\Windows.Storage.Streams.h>
#include <winrt\Windows.Web.Http.h>
#include <winrt\Windows.Web.Http.Headers.h>

using std::wcout, std::endl;
using std::shared_ptr, std::atomic_bool, std::atomic_uint64_t;
using winrt::hstring;
using namespace Windows::Security::Cryptography;
using namespace Windows::Security::Cryptography::Core;
using namespace Windows::Data::Json;
using namespace Windows::Storage;
using namespace Windows::Storage::Streams;
using namespace Windows::Web::Http;

IStorageFolder SetupTemporaryDirectory() {
    const auto& dirName = L"vscode-installer-" + winrt::to_hstring(GuidHelper::CreateNewGuid());
    return StorageFolder::GetFolderFromPathAsync(std::filesystem::temp_directory_path().c_str()).get()
        .CreateFolderAsync(dirName, CreationCollisionOption::OpenIfExists).get();
}

ReleaseInfo GetReleaseInfo(const hstring& archPkg) {
    const auto& apiUrl{ hstring{L"https://update.code.visualstudio.com/api/update/win32-" } + archPkg + L"/stable/latest" };
    wcout << L"Requesting hash from " << apiUrl.data() << '.' << endl;

    HttpClient client;
    client.DefaultRequestHeaders().Insert(L"User-Agent", L"cli/vscode-winsta11er");

    HttpRequestMessage jsonReqMsg;
    jsonReqMsg.RequestUri(Uri{ apiUrl });
    const auto& getJsonRes = GetResultsWithTimeout(client.SendRequestAsync(jsonReqMsg, HttpCompletionOption::ResponseContentRead), 30s);
    getJsonRes.EnsureSuccessStatusCode();
    hstring jsonString{ getJsonRes.Content().ReadAsStringAsync().get() };
    client.Close();

    const auto& json = JsonObject::Parse(jsonString);
    ReleaseInfo info{
        json.GetNamedString(L"url"),
        json.GetNamedString(L"name"),
        json.GetNamedString(L"sha256hash")
    };
    return info;
}

winrt::fire_and_forget SlowDownloadWatchdog(
    shared_ptr<atomic_bool> shouldQuit,
    shared_ptr<atomic_uint64_t> readLen,
    uint64_t totalLen
) {
    while (*readLen < totalLen && !*shouldQuit) {
        uint64_t lastReadLen = *readLen;
        co_await winrt::resume_after(5s);
        uint64_t currentReadLen = *readLen, read = currentReadLen - lastReadLen;
        if (read < 200 && currentReadLen < totalLen) {
            wcout << L"stream stalled : received " << read << L" bytes over the last % d seconds" << endl;
            *shouldQuit = true;
        }
    }
}

IStorageFile DownloadInstaller(const IStorageFolder& installerDir, const hstring& archPkg, ReleaseInfo&& info) {
    wcout << L"Downloading installer from " << info.Url.c_str() << '.' << endl;

    hstring fileName{ hstring { L"vscode-win32-" } + archPkg + L".exe" };
    const auto& file = installerDir.CreateFileAsync(fileName, CreationCollisionOption::ReplaceExisting).get();
    const auto& fileStream = file.OpenAsync(FileAccessMode::ReadWrite).get();

    HttpClient client;
    client.DefaultRequestHeaders().Insert(L"User-Agent", L"cli/vscode-winsta11er");

    HttpRequestMessage dataReqMsg;
    dataReqMsg.RequestUri(Uri{ std::move(info.Url) });
    const auto& getDataRes = GetResultsWithTimeout(client.SendRequestAsync(dataReqMsg, HttpCompletionOption::ResponseHeadersRead), 60s);
    getDataRes.EnsureSuccessStatusCode();
    const auto& content = getDataRes.Content();
    const auto totalLen = content.Headers().ContentLength().Value();
    const auto& netStream = content.ReadAsInputStreamAsync().get();

    auto shouldQuit{ std::make_shared<atomic_bool>() };
    auto readLen{ std::make_shared<atomic_uint64_t>() };
    SlowDownloadWatchdog(shouldQuit, readLen, totalLen);
    const auto& hasher = HashAlgorithmProvider::OpenAlgorithm(HashAlgorithmNames::Sha256()).CreateHash();

    IBuffer readBuf, buf{ CryptographicBuffer::CreateFromByteArray(std::vector<uint8_t>(32 << 10)) };
    do {
        readBuf = GetResultsWithTimeout(netStream.ReadAsync(buf, 32 << 10, InputStreamOptions::Partial), 5s);
        const auto read = static_cast<uint64_t>(readBuf.Length());
        if (read == 0) {
            break;
        }
        *readLen += read;
        const auto& writeTask = fileStream.WriteAsync(readBuf);
        hasher.Append(readBuf);
        writeTask.get();
    } while (*readLen < totalLen && !*shouldQuit);

    if (*readLen < totalLen) {
        if (*shouldQuit) {
            throw winrt::hresult_error(E_FAIL, L"Less than 200 bytes retrieved in 5 seconds.");
        }
        else {
            *shouldQuit = true;
            winrt::check_win32(WSAECONNRESET);
        }
    }
    *shouldQuit = true;

    netStream.Close();
    client.Close();
    const auto& flushTask = fileStream.FlushAsync();

    const bool hashMatch = CryptographicBuffer::Compare(hasher.GetValueAndReset(), CryptographicBuffer::DecodeFromHexString(info.Sha256Hash));
    if (!hashMatch) {
        throw winrt::hresult_error(E_FAIL, L"Checksum mismatch");
    }
    flushTask.get();
    fileStream.Close();

    wcout << L"Downloaded installer to file " << file.Path().data() << '.' << endl;
    return file;
}

void RunInstaller(const IStorageFile& installerFile) {
    const auto& installerPath = installerFile.Path();
    std::vector<wchar_t> arg;
    arg.reserve(static_cast<size_t>(installerPath.size()) + 1 + INSTALLER_ARG_SIZE + 1);
    arg.insert(arg.end(), installerPath.data(), installerPath.data() + installerPath.size());
    arg.push_back(' ');
    arg.insert(arg.end(), INSTALLER_ARG, INSTALLER_ARG + INSTALLER_ARG_SIZE + 1);
    STARTUPINFO si;
    PROCESS_INFORMATION pi;
    ZeroMemory(&si, sizeof(si));
    si.cb = sizeof(si);
    ZeroMemory(&pi, sizeof(pi));

    winrt::check_bool(CreateProcess(installerFile.Path().c_str(), arg.data(), nullptr, nullptr, true, 0, nullptr, nullptr, &si, &pi));

    WaitForSingleObject(pi.hProcess, INFINITE);

    DWORD exitCode{};
    winrt::check_bool(GetExitCodeProcess(pi.hProcess, &exitCode));
    wcout << L"Installer exited with code " << exitCode << '.' << endl;

    CloseHandle(pi.hProcess);
    CloseHandle(pi.hThread);
}

void Cleanup(const IStorageFolder& installerDir) {
    installerDir.DeleteAsync(StorageDeleteOption::PermanentDelete).get();
}

int WINAPI wWinMain(
    [[maybe_unused]] HINSTANCE hInstance,
    [[maybe_unused]] HINSTANCE hPrevInstance,
    [[maybe_unused]] PWSTR pCmdLine,
    [[maybe_unused]] int nCmdShow
) {
    init_apartment();
    winrt::check_bool(_setmode(_fileno(stdout), _O_WTEXT) > 0);
    try {
        hstring archPkg{ winrt::to_hstring(ARCH_PKG) };
        const auto& installerDir = SetupTemporaryDirectory();
        ReleaseInfo info{ GetReleaseInfo(archPkg) };
        const auto& installerFile = DownloadInstaller(installerDir, archPkg, std::move(info));
        RunInstaller(installerFile);
        Cleanup(installerDir);
        return 0;
    }
    catch (...) {
        wcout << winrt::to_message().c_str() << endl;
        return winrt::to_hresult();
    }
}
