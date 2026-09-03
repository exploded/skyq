@echo off
rem Task Scheduler wrapper for the morning report. Schedule this file daily
rem at 09:00 ("Start in" is handled by the cd below, so the task needs no
rem Start-in field). Console output is lost under the scheduler, so append
rem everything to a log next to the exe.
cd /d %~dp0
skyq.exe report >> skyq-report.log 2>&1
