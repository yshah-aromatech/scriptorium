<#
.SYNOPSIS
Does a thing.
.PARAMETER Mode
The mode to run in.
#>
param(
    [Parameter(Mandatory)][ValidateSet('a', 'b')][string]$Mode,
    [switch]$Verbose2,
    [string]$Name = 'default'
)
Write-Host hi
