BeforeAll {
    Import-Module (Join-Path $PSScriptRoot '../src/Core.psm1') -Force -DisableNameChecking
}

Describe 'Read-PssEnvFile' {
    BeforeEach {
        $script:envFile = Join-Path ([IO.Path]::GetTempPath()) "pss-test-$(New-Guid).env"
    }
    AfterEach {
        Remove-Item $script:envFile -Force -ErrorAction SilentlyContinue
    }

    It 'parses KEY=VALUE lines' {
        "A=1`nB=two words" | Set-Content $script:envFile
        $r = Read-PssEnvFile $script:envFile
        $r['A'] | Should -Be '1'
        $r['B'] | Should -Be 'two words'
    }

    It 'skips comments and blank lines' {
        "# comment`n`nA=1" | Set-Content $script:envFile
        (Read-PssEnvFile $script:envFile).Count | Should -Be 1
    }

    It 'strips matched quotes' {
        "A='quoted'`nB=`"dquoted`"" | Set-Content $script:envFile
        $r = Read-PssEnvFile $script:envFile
        $r['A'] | Should -Be 'quoted'
        $r['B'] | Should -Be 'dquoted'
    }

    It 'keeps = signs inside values' {
        'A=x=y' | Set-Content $script:envFile
        (Read-PssEnvFile $script:envFile)['A'] | Should -Be 'x=y'
    }

    It 'returns empty for a missing file' {
        (Read-PssEnvFile "$script:envFile-nope").Count | Should -Be 0
    }
}

Describe 'Register-PssSecret / Hide-PssSecret' {
    It 'redacts values whose names look secret-ish' {
        Register-PssSecret -Name 'MY_API_TOKEN' -Value 'supersecret123'
        Hide-PssSecret 'the value is supersecret123 ok' | Should -Be 'the value is *** ok'
    }

    It 'ignores non-secret-looking names without -Force' {
        Register-PssSecret -Name 'GREETING' -Value 'hello-world-value'
        Hide-PssSecret 'hello-world-value' | Should -Be 'hello-world-value'
    }

    It 'registers any name with -Force' {
        Register-PssSecret -Name 'GREETING' -Value 'forced-secret-value' -Force
        Hide-PssSecret 'x forced-secret-value y' | Should -Be 'x *** y'
    }

    It 'ignores short values' {
        Register-PssSecret -Name 'SHORT_TOKEN' -Value 'abc' -Force
        Hide-PssSecret 'abc' | Should -Be 'abc'
    }

    It 'matches broadened name patterns (PASS, CONN, DSN, AUTH)' {
        Register-PssSecret -Name 'DB_CONN' -Value 'connstring-value-1'
        Register-PssSecret -Name 'SMTP_PASS' -Value 'smtppass-value-22'
        Hide-PssSecret 'connstring-value-1 smtppass-value-22' | Should -Be '*** ***'
    }
}

Describe 'Split-PssArguments' {
    It 'splits on whitespace' {
        Split-PssArguments 'a b  c' | Should -Be @('a', 'b', 'c')
    }

    It 'keeps double-quoted groups together' {
        Split-PssArguments '-Message "hello world" -Flag' | Should -Be @('-Message', 'hello world', '-Flag')
    }

    It 'keeps single-quoted groups together' {
        Split-PssArguments "-Msg 'a b'" | Should -Be @('-Msg', 'a b')
    }

    It 'supports empty quoted tokens' {
        Split-PssArguments 'a "" b' | Should -Be @('a', '', 'b')
    }

    It 'returns an empty array for blank input' {
        @(Split-PssArguments '   ').Count | Should -Be 0
        @(Split-PssArguments $null).Count | Should -Be 0
    }

    It 'handles adjacent quoted and unquoted text' {
        Split-PssArguments "pre'mid dle'post" | Should -Be @('premid dlepost')
    }
}

Describe 'Get-PssDisplayWidth / Format-PssCell' {
    It 'counts ASCII as 1 cell each' {
        Get-PssDisplayWidth 'hello' | Should -Be 5
    }

    It 'counts CJK as 2 cells each' {
        Get-PssDisplayWidth '日本語' | Should -Be 6
    }

    It 'counts emoji as 2 cells' {
        Get-PssDisplayWidth '🎉ok' | Should -Be 4
    }

    It 'counts combining marks as 0 cells' {
        Get-PssDisplayWidth "e$([char]0x0301)" | Should -Be 1
    }

    It 'pads to exact width' {
        (Format-PssCell -Text 'ab' -Width 5).Length | Should -Be 5
    }

    It 'truncates wide chars without splitting cells' {
        $r = Format-PssCell -Text '日本語' -Width 5
        Get-PssDisplayWidth $r | Should -Be 5
        $r | Should -Be '日本 '
    }

    It 'adds an ellipsis when truncating with -Ellipsis' {
        Format-PssCell -Text 'abcdef' -Width 4 -Ellipsis | Should -Be 'abc…'
    }
}

Describe 'Format-PssDuration / Format-PssRelativeTime' {
    It 'formats seconds' { Format-PssDuration 5.26 | Should -Be '5.3s' }
    It 'formats minutes' { Format-PssDuration 90 | Should -Be '1m30s' }
    It 'formats hours' { Format-PssDuration 3725 | Should -Be '1h02m05s' }
    It 'formats relative times compactly' {
        Format-PssRelativeTime 45 | Should -Be '45s'
        Format-PssRelativeTime 300 | Should -Be '5m'
        Format-PssRelativeTime 90000 | Should -Be '1d'
    }
}

Describe 'ConvertTo-Ansi256Index' {
    It 'maps pure gray to the gray ramp' {
        $idx = ConvertTo-Ansi256Index 128 128 128
        $idx | Should -BeGreaterOrEqual 232
    }
    It 'maps saturated colors into the cube' {
        $idx = ConvertTo-Ansi256Index 255 0 0
        $idx | Should -Be 196
    }
}

Describe 'Initialize-Pss config handling' {
    BeforeEach {
        $script:appDir = Join-Path ([IO.Path]::GetTempPath()) "pss-app-$(New-Guid)"
        $script:dataDir = Join-Path $script:appDir 'data'
        New-Item -ItemType Directory -Path $script:appDir -Force | Out-Null
    }
    AfterEach {
        Remove-Item $script:appDir -Recurse -Force -ErrorAction SilentlyContinue
    }

    It 'warns about unknown config keys' {
        @{ dataDir = $script:dataDir; notAKey = 1 } | ConvertTo-Json | Set-Content (Join-Path $script:appDir 'config.json')
        Initialize-Pss -AppDir $script:appDir
        @(Get-PssConfigWarnings)[0] | Should -Match 'notAKey'
    }

    It 'warns and keeps the default when a numeric key is not numeric' {
        @{ dataDir = $script:dataDir; runTimeoutMinutes = 'lots' } | ConvertTo-Json | Set-Content (Join-Path $script:appDir 'config.json')
        Initialize-Pss -AppDir $script:appDir
        @(Get-PssConfigWarnings).Count | Should -Be 1
        (Get-PssConfig).runTimeoutMinutes | Should -Be 0
    }

    It 'creates the data directories including locks' {
        @{ dataDir = $script:dataDir } | ConvertTo-Json | Set-Content (Join-Path $script:appDir 'config.json')
        Initialize-Pss -AppDir $script:appDir
        Test-Path (Get-PssPaths).LocksDir | Should -BeTrue
    }

    It 'prunes old logs and caps history at startup' {
        @{ dataDir = $script:dataDir; logRetentionDays = 7; historyMaxLines = 5 } | ConvertTo-Json |
            Set-Content (Join-Path $script:appDir 'config.json')
        Initialize-Pss -AppDir $script:appDir
        $paths = Get-PssPaths

        $old = Join-Path $paths.LogsDir 'old.log'
        $new = Join-Path $paths.LogsDir 'new.log'
        'x' | Set-Content $old; 'x' | Set-Content $new
        (Get-Item $old).LastWriteTime = (Get-Date).AddDays(-30)
        1..20 | ForEach-Object { "{""script"":""s$_""}" } | Set-Content $paths.HistoryFile

        Clear-PssOldData
        Test-Path $old | Should -BeFalse
        Test-Path $new | Should -BeTrue
        @(Get-Content $paths.HistoryFile).Count | Should -Be 5
        (Get-Content $paths.HistoryFile | Select-Object -Last 1) | Should -Match 's20'
    }
}
