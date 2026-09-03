@echo off
rem Task Scheduler wrapper for the Phase 2 live monitor. Schedule at startup
rem (restart on failure) as the user N.I.N.A. runs as. Console output is
rem lost under the scheduler, so append everything to a log next to the exe.
cd /d %~dp0
skyq.exe serve >> skyq-serve.log 2>&1
