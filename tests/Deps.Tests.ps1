BeforeAll {
    Import-Module (Join-Path $PSScriptRoot '../src/Core.psm1') -Force -DisableNameChecking
    Import-Module (Join-Path $PSScriptRoot '../src/Deps.psm1') -Force -DisableNameChecking

    function New-TestScript {
        param([string]$Source)
        $dir = Join-Path ([IO.Path]::GetTempPath()) "pss-deps-$(New-Guid)"
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
        $entry = Join-Path $dir 'main.ps1'
        $Source | Set-Content $entry
        [pscustomobject]@{
            Name = 'test'; Dir = $dir; Entry = $entry
            ModuleDir = Join-Path $dir 'mods'
        }
    }
}

Describe 'Get-PssScriptDeps' {
    It 'finds Import-Module by name' {
        $s = New-TestScript 'Import-Module Az.Accounts'
        @(Get-PssScriptDeps -Script $s).Name | Should -Be 'Az.Accounts'
        Remove-Item $s.Dir -Recurse -Force
    }

    It 'does not mistake parameter values for module names' {
        $s = New-TestScript 'Import-Module PSSQLite -ErrorAction Stop -ErrorVariable ev'
        @(Get-PssScriptDeps -Script $s).Name | Should -Be 'PSSQLite'
        Remove-Item $s.Dir -Recurse -Force
    }

    It 'finds #Requires modules with version constraints' {
        $s = New-TestScript "#Requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.0' }`nWrite-Host hi"
        $deps = @(Get-PssScriptDeps -Script $s)
        $deps.Name | Should -Be 'Pester'
        $deps[0].MinimumVersion | Should -Be '5.0'
        $deps[0].Display | Should -Be 'Pester (>=5.0)'
        Remove-Item $s.Dir -Recurse -Force
    }

    It 'finds using module statements' {
        $s = New-TestScript "using module dbatools`nWrite-Host hi"
        @(Get-PssScriptDeps -Script $s).Name | Should -Be 'dbatools'
        Remove-Item $s.Dir -Recurse -Force
    }

    It 'excludes builtins and local files' {
        $s = New-TestScript "Import-Module Microsoft.PowerShell.Utility`nImport-Module ./local.psm1`nImport-Module Helper"
        'x' | Set-Content (Join-Path $s.Dir 'Helper.psm1')
        @(Get-PssScriptDeps -Script $s).Count | Should -Be 0
        Remove-Item $s.Dir -Recurse -Force
    }

    It 'maps common name mismatches' {
        $s = New-TestScript 'Import-Module sqlps'
        @(Get-PssScriptDeps -Script $s).Name | Should -Be 'SqlServer'
        Remove-Item $s.Dir -Recurse -Force
    }

    It 'handles array imports' {
        $s = New-TestScript 'Import-Module ModA, ModB'
        @(Get-PssScriptDeps -Script $s).Name | Should -Be @('ModA', 'ModB')
        Remove-Item $s.Dir -Recurse -Force
    }
}

Describe 'Test-PssDepSatisfied' {
    It 'is satisfied by presence when unversioned' {
        $installed = @{ 'ModA' = [System.Collections.Generic.List[version]]@([version]'1.0') }
        Test-PssDepSatisfied -Dep (New-PssDep -Name 'ModA') -Installed $installed | Should -BeTrue
    }
    It 'is unsatisfied when absent' {
        Test-PssDepSatisfied -Dep (New-PssDep -Name 'ModB') -Installed @{} | Should -BeFalse
    }
    It 'checks exact RequiredVersion' {
        $installed = @{ 'ModA' = [System.Collections.Generic.List[version]]@([version]'1.0') }
        Test-PssDepSatisfied -Dep (New-PssDep -Name 'ModA' -RequiredVersion '1.0') -Installed $installed | Should -BeTrue
        Test-PssDepSatisfied -Dep (New-PssDep -Name 'ModA' -RequiredVersion '2.0') -Installed $installed | Should -BeFalse
    }
    It 'checks minimum/maximum bounds' {
        $installed = @{ 'ModA' = [System.Collections.Generic.List[version]]@([version]'1.5') }
        Test-PssDepSatisfied -Dep (New-PssDep -Name 'ModA' -MinimumVersion '1.0') -Installed $installed | Should -BeTrue
        Test-PssDepSatisfied -Dep (New-PssDep -Name 'ModA' -MinimumVersion '2.0') -Installed $installed | Should -BeFalse
        Test-PssDepSatisfied -Dep (New-PssDep -Name 'ModA' -MaximumVersion '1.0') -Installed $installed | Should -BeFalse
    }
}

Describe 'Get-PssInstallCommand' {
    It 'embeds version constraints in the generated command' {
        $s = New-TestScript 'Write-Host hi'
        $cmd = Get-PssInstallCommand -Script $s -Modules @((New-PssDep -Name 'Pester' -RequiredVersion '5.8.0'))
        $cmd | Should -Match "Rv='5\.8\.0'"
        $cmd | Should -Match "Name='Pester'"
        Remove-Item $s.Dir -Recurse -Force
    }

    It 'accepts plain strings for backward compatibility' {
        $s = New-TestScript 'Write-Host hi'
        $cmd = Get-PssInstallCommand -Script $s -Modules @('SomeModule')
        $cmd | Should -Match "Name='SomeModule'"
        Remove-Item $s.Dir -Recurse -Force
    }
}
