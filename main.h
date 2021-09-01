/*---------------------------------------------------------------------------------------------
 *  Copyright (c) Microsoft Corporation. All rights reserved.
 *  Licensed under the MIT License. See License.txt in the project root for license information.
 *--------------------------------------------------------------------------------------------*/

#pragma once

#include <windows.h>
#include <winrt\Windows.Foundation.h>

using namespace winrt;
using namespace Windows::Foundation;
using namespace std::literals::chrono_literals;

struct ReleaseInfo {
    winrt::hstring Url;
    winrt::hstring Name;
    winrt::hstring Sha256Hash;
};

#ifdef _M_X64
constexpr const wchar_t* ARCH_PKG = L"x64-user";
#endif
#ifdef _M_IX86
constexpr const wchar_t* ARCH_PKG = L"user";
#endif
#ifdef _M_ARM64
constexpr const wchar_t* ARCH_PKG = L"arm64-user";
#endif
constexpr const wchar_t* INSTALLER_ARG = L"/verysilent /mergetasks=!runcode";
constexpr const auto INSTALLER_ARG_SIZE = std::char_traits<wchar_t>::length(INSTALLER_ARG);

template<typename T>
auto GetResultsWithTimeout(const T& asyncAction, const TimeSpan& timeout) {
    if (asyncAction.wait_for(timeout) == AsyncStatus::Started) {
        winrt::throw_hresult({ ERROR_TIMEOUT });
    }
    return asyncAction.GetResults();
}


