@echo off
rem Запуск реестра одной командой: собрать, поднять, открыть в браузере.
rem
rem Скрипт существует ради одного: посмотреть на реестр, не вспоминая ни флагов,
rem ни адреса, ни того, где на этой машине лежит Go.
rem
rem Файл сохранен в кодировке cp866: cmd.exe читает пакетные файлы в кодировке
rem OEM, и на UTF-8 строки с кириллицей рвутся, а их куски исполняются как
rem команды.
setlocal

cd /d "%~dp0"

rem Go ищем сам. На машине, где он поставлен из GoLand, его нет в PATH, и
rem "go не является внутренней или внешней командой" - первое, обо что
rem спотыкается запуск.
set "GO="
for %%g in (go.exe) do if not "%%~$PATH:g"=="" set "GO=go"
if not defined GO if exist "%ProgramFiles%\Go\bin\go.exe" set "GO=%ProgramFiles%\Go\bin\go.exe"
if not defined GO if exist "%LOCALAPPDATA%\Programs\Go\bin\go.exe" set "GO=%LOCALAPPDATA%\Programs\Go\bin\go.exe"
if not defined GO for /d %%d in ("%USERPROFILE%\sdk\go*") do if exist "%%d\bin\go.exe" set "GO=%%d\bin\go.exe"

if not defined GO (
  echo.
  echo   Go не найден. Поставьте его с https://go.dev/dl/ или добавьте в PATH.
  echo.
  pause
  exit /b 1
)

if not exist ".env" (
  if exist ".env.example" (
    echo Нет файла .env - копирую из .env.example.
    echo Ключи в нем не заполнены: Bitrix24 и модель работать не будут,
    echo реестр поднимется на ручном разборе.
    copy /y ".env.example" ".env" >nul
  )
)

rem Порт можно перебить: run.cmd 9090
set "PORT=%~1"
if not defined PORT set "PORT=8080"

echo.
echo   Реестр: http://127.0.0.1:%PORT%
echo   Вход: логины и пароли из REESTR_USERS в файле .env
echo   Остановить: Ctrl+C
echo.

rem -web=web отдает интерфейс с диска, а не из бинарника: правку в css или js
rem видно после обновления страницы, без пересборки. Для показа это удобнее, а
rem для боевого запуска флаг не нужен - там интерфейс вшит внутрь.
"%GO%" run ./cmd/reestr -addr=127.0.0.1:%PORT% -web=web

endlocal
