# sdterm.zsh
# Author: Kennt Kim
# Created: 2026-10-19 · Last updated: 2026-10-30
#
# Purpose: the terminal engine's shell integration for zsh (SiliconDesk's SDTERM-2.md §4.3,
# PROTOCOL.md §7.7): marks each prompt, command line, command output and command end with OSC 133,
# and reports the working directory with OSC 7.
#
# Notes:
# - Taken from SiliconDesk's host/internal/shellinteg/scripts/zsh/sdterm.zsh on
#   2026-10-23. Where these notes say "the session", read whoever starts the shell: the
#   engine's user (Docs/design/02-CONNECTIONS.md §1).
# - Loaded by .zshenv, before the user's .zshrc: the hooks are installed at the first prompt, after
#   the startup files, so a theme that marks prompts itself (Powerlevel10k's shell integration,
#   `]133;` in PS1) is left alone. On that first prompt a theme's own precmd may set PS1 after ours,
#   leaving it unmarked; every later prompt is marked.
# - D;$? is the first precmd (it hands $? on to the others); A and B go around PS1 in the last one,
#   wherever a theme moved them; C comes from preexec with the command line (`cmdline_url`,
#   percent-encoded) and its start (`sdterm_started`, Unix milliseconds from zsh/datetime), so its
#   duration survives a session change.
# - aid is the shell's pid: an inner shell's marks (ssh to a host with integration) are told apart.
# - The directory is reported at every prompt, not only when it changes: a prompt's mark carries the
#   last one reported (paths in a command's output are read from it), and after `ssh` to a host
#   whose shell reported its own, this shell's prompts would otherwise have none.

builtin autoload -Uz add-zsh-hook
builtin zmodload zsh/datetime 2>/dev/null

typeset -g _sdterm_aid=$$
typeset -g _sdterm_a=$'%{\e]133;A;aid='$$$'\a%}'
typeset -g _sdterm_b=$'%{\e]133;B\a%}'
typeset -g _sdterm_pwd=
typeset -g _sdterm_pwd_url= # the directory as reported, kept: _sdterm_url is preexec's too
typeset -g _sdterm_url=

# Percent-encodes $1 into _sdterm_url.
_sdterm_urlencode() {
    builtin emulate -L zsh
    builtin local LC_ALL=C s=$1 out= c hex
    builtin integer i
    for (( i = 1; i <= ${#s}; i++ )); do
        c=$s[i]
        if [[ $c == [a-zA-Z0-9._~/-] ]]; then
            out+=$c
        else
            builtin printf -v hex '%%%02X' "'$c"
            out+=$hex
        fi
    done
    _sdterm_url=$out
}

_sdterm_report_cwd() {
    if [[ $PWD != $_sdterm_pwd ]]; then
        _sdterm_pwd=$PWD
        _sdterm_urlencode $PWD
        _sdterm_pwd_url=$_sdterm_url
    fi
    builtin printf '\e]7;file://%s%s\a' $HOST $_sdterm_pwd_url
}

# First precmd: the last command's end, and the directory (encoded again only when it changed).
_sdterm_precmd() {
    builtin local ret=$?
    builtin printf '\e]133;D;%s;aid=%s\a' $ret $_sdterm_aid
    _sdterm_report_cwd
    return $ret
}

# Last precmd: A and B around PS1.
_sdterm_prompt() {
    builtin local ret=$?
    PS1=${PS1//$_sdterm_a/}
    PS1=${PS1//$_sdterm_b/}
    PS1=$_sdterm_a$PS1$_sdterm_b
    return $ret
}

_sdterm_preexec() {
    _sdterm_urlencode $1
    builtin printf '\e]133;C;aid=%s;sdterm_started=%s;cmdline_url=%s\a' $_sdterm_aid \
        $(( ${epochtime[1]:-0} * 1000 + ${epochtime[2]:-0} / 1000000 )) $_sdterm_url
}

# The first prompt: install the hooks, after the startup files.
_sdterm_install() {
    builtin local ret=$?
    add-zsh-hook -d precmd _sdterm_install
    if [[ $PS1 != *']133;'* ]]; then
        precmd_functions=(_sdterm_precmd ${precmd_functions[@]} _sdterm_prompt)
        add-zsh-hook preexec _sdterm_preexec
        _sdterm_prompt
    fi
    _sdterm_report_cwd
    return $ret
}
add-zsh-hook precmd _sdterm_install
