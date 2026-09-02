<#
.SYNOPSIS
Does the thing.
.DESCRIPTION
A longer description of the thing.
.PARAMETER Who
Who to greet.
#>
param([Parameter(Mandatory)][string]$Who, [switch]$DryRun)
Write-Output hi
