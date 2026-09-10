import hashlib
import os
import shutil
import subprocess
import tempfile
import time
from pathlib import Path

import requests

ROOT = Path(__file__).resolve().parent
LEGACY = ROOT / "GD-Fiscal-Saude-MVP-LEGACY.exe"
REVISED = ROOT / "GD-Fiscal-Saude-REVISAO-1.1.1.exe"
BASE = "http://127.0.0.1:17654"
TMP = Path(tempfile.mkdtemp(prefix="gd-live-upgrade-"))
LOCAL = TMP / "localappdata"
LOCAL.mkdir(parents=True, exist_ok=True)
CAPTURE = TMP / "browser.txt"


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def env():
    e = os.environ.copy()
    e.pop("GD_FISCAL_SAUDE_TEST_MODE", None)
    e["LOCALAPPDATA"] = str(LOCAL)
    e["GD_FISCAL_SAUDE_TEST_BROWSER_CAPTURE"] = str(CAPTURE)
    return e


def wait_health(expect=True, timeout=12):
    end = time.time() + timeout
    last = None
    while time.time() < end:
        try:
            r = requests.get(BASE + "/health", timeout=0.5)
            alive = r.status_code == 200
        except Exception as exc:
            last = exc
            alive = False
        if alive == expect:
            return
        time.sleep(0.2)
    raise AssertionError(f"health did not become {expect}; last={last}")


def kill_installed():
    subprocess.run(
        ["taskkill", "/IM", "GD-Fiscal-Saude.exe", "/F"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )
    time.sleep(0.5)


try:
    kill_installed()
    assert LEGACY.exists(), LEGACY
    assert REVISED.exists(), REVISED

    # Real launch path: the downloaded legacy EXE installs itself and leaves the
    # installed GD-Fiscal-Saude.exe serving the fixed local port.
    subprocess.run([str(LEGACY)], env=env(), timeout=10, check=True)
    wait_health(True)

    old_page = requests.get(BASE + "/payments", timeout=3).text
    assert "Validado para Nota Fiscal" not in old_page

    installed = LOCAL / "Programs" / "GD Fiscal Saude" / "GD-Fiscal-Saude.exe"
    assert installed.exists(), installed
    old_hash = sha256(installed)

    # User's reported scenario: execute revised download while old installed
    # server is still alive in the background.
    subprocess.run([str(REVISED)], env=env(), timeout=10, check=True)
    time.sleep(1.0)
    wait_health(True)

    page = requests.get(BASE + "/payments", timeout=3).text
    new_hash = sha256(installed)

    # These are the regression assertions. Current updater must fail here:
    # it must replace the installed binary and expose the revised UI.
    assert new_hash == sha256(REVISED), (old_hash, new_hash, sha256(REVISED))
    assert "Validado para Receita Saúde" in page
    assert "Validado para Nota Fiscal" in page
    assert "Não lançado" in page
    assert "Confirmar lançados no Receita Saúde" in page

    print("LIVE_UPGRADE_GATE_GREEN")
finally:
    kill_installed()
    shutil.rmtree(TMP, ignore_errors=True)
