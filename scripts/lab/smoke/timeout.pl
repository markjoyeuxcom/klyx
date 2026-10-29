#!/usr/bin/perl
# usage: perl to.pl <seconds> <command> [args...]
# Runs the command in its own process group and kills the group after the
# deadline (fork and kill: Go binaries ignore SIGALRM). Exit status is the
# command's, or 124 on timeout.
use strict;
use warnings;
use POSIX ":sys_wait_h";
my $secs = shift @ARGV;
my $pid = fork();
die "fork: $!" unless defined $pid;
if ($pid == 0) {
    setpgrp(0, 0);
    exec { $ARGV[0] } @ARGV or die "exec $ARGV[0]: $!";
}
my $deadline = time + $secs;
while (1) {
    my $r = waitpid($pid, WNOHANG);
    if ($r == $pid) {
        exit(($? & 127) ? 128 + ($? & 127) : $? >> 8);
    }
    if (time >= $deadline) {
        kill 'TERM', -$pid;
        sleep 2;
        kill 'KILL', -$pid;
        waitpid($pid, 0);
        print STDERR "to.pl: timed out after ${secs}s: @ARGV\n";
        exit 124;
    }
    select(undef, undef, undef, 0.1);
}
