# ee-setup — ADB, SSH и root на Alcatel EE71 / EE72

Один бинарник на Go, который превращает стоковый роутер Alcatel EE71 или EE72 в
управляемое устройство: работающий ADB, который сохраняется после перезагрузки,
SSH-сервер dropbear и известный пароль root.

Без утилиты TCL для Windows, без установки драйверов и без Python.

## Запуск

```sh
ee-setup
```

Это весь интерфейс. Программа находит роутер, запрашивает пароль администратора,
выполняет изменения, предварительно сделав backup, печатает каждый шаг и
предлагает перезагрузить устройство, чтобы включился ADB. Пароль root/SSH по
умолчанию — `oemlinux1`.

Флаги для неинтерактивного запуска:

```sh
--host IP            не искать устройство автоматически
--password PW        пароль администратора вместо запроса
--root-password PW   пароль root/SSH (по умолчанию: oemlinux1)
```

## Что делает программа

### EE72

В обычной композиции EE72 ADB недоступен, зато веб-API принимает
аутентифицированные команды. `ee-setup` логинится и через этот канал:

1. сохраняет `/systemrw/boot_hsusb_comp`, `/etc/shadow` и lock-файл adbd в
   `/data/jrd/usr/eesetup-<stamp>/`;
2. перемонтирует `/` в rw описанным помощником через modprobe и возвращает `ro`
   при выходе (в том числе при ошибке);
3. удаляет `/jrd-resource/resource/lock_flag`, после чего adbd принимает
   сервисы `shell:`, `exec:` и `sync:`;
4. задаёт пароль root через `chpasswd` (с запасным вариантом `passwd`);
5. записывает `0196` в `/systemrw/boot_hsusb_comp`, чтобы начиная со следующей
   загрузки использовалась отладочная композиция с ADB.

Загрузочная цепочка, разделы MTD, recovery и efs2 не затрагиваются.

### EE71

Сначала нужен переключатель USB mass-storage. `ee-setup` сам отправляет
вендорную SCSI-команду, ждёт ADB и устанавливает из встроенных ресурсов:

- dropbear (`dropbearmulti`) в `/system/usr/sbin`, init-скрипты и rc-ссылки;
- init-скрипт `patch_usb_kernel`, который сохраняет ADB в USB-композиции после
  перезагрузки;
- пароль root.

Если роутер в режиме mass-storage: выключить, зажать RESET, подключить USB,
отпустить при мигании индикаторов и снова запустить `ee-setup`.

## Требования

- **adb** в `PATH` (Android Platform Tools) — для EE71. Для EE72 adb нужен только
  после перезагрузки.
- **Права администратора**: порт 80 используется для отдачи скрипта устройству,
  а переключение USB на EE71 требует доступа к USB.
- **dropbear для EE72** опционален и не входит в сборку. Положите бинарник
  ARMv7 **hard-float** с именем `dropbear` рядом с исполняемым файлом
  `ee-setup` — он будет установлен и запущен автоматически. Soft-float
  бинарники от EE71 на EE72 не работают: прошивка `armv7l` hard-float.

## Безопасность

- Перед первой записью создаётся каталог с резервной копией.
- Скрипт содержит trap, который возвращает `/` в readonly даже при ошибке.
- Устройство в режиме Qualcomm EDL (`05c6:9008`) не затрагивается никогда.
- Ничего не записывается, пока не выполнен вход администратора.

## Откат

На устройстве (через ADB или SSH):

```sh
# EE72
cat /data/jrd/usr/eesetup-<stamp>/boot_hsusb_comp > /systemrw/boot_hsusb_comp
cat /data/jrd/usr/eesetup-<stamp>/shadow > /etc/shadow
cp /data/jrd/usr/eesetup-<stamp>/lock_flag /jrd-resource/resource/lock_flag
```

Для EE71 удалить `/system/etc/rcS.d/S01patch_usb_kernel` и восстановить
`/system/sbin/usb/boot_hsusb_composition` из резервной копии.

## Сборка

```sh
./build.sh assets
./build.sh build            # для текущей ОС
./build.sh build windows    # без CGO, libusb не нужен
VERSION=v0.1.0 ./build.sh release   # готовые архивы в dist/
```

Матрица релиза следует правилам кросс-компиляции Go:

| Цель | CGO | USB-переключение EE71 |
|---|---|---|
| darwin/arm64 | вкл (libusb) | да |
| darwin/amd64 | вкл (libusb) | да |
| windows/amd64 | выкл | да (Win32 SCSI, libusb не нужен) |
| linux/amd64 | выкл | нет — путь EE72 через web-API работает |

Сборка без cgo на macOS/Linux полностью поддерживает EE72; libusb нужен только
для переключения USB на EE71. Бэкенд выбирается build-тегами
(`switch_unix.go` и `switch_nocgo.go`), без проверок во время работы.

## Статус

Процедура для EE72 соответствует `docs/ee72_access_method.md` (прошивка
`EE72E_E1_01.00_04`). Путь EE71 повторяет процедуры из `github/ee71-update` и
`github/dropbear`. Проверка на железе — следующий шаг.
