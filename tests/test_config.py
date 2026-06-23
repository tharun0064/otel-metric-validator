"""Unit tests for the .env loader (inline-comment + quote handling)."""

import os

from validator.config import load_config, load_dotenv

_BASE_ENV = {
    "ORACLE_HOST": "h", "ORACLE_SERVICE": "s",
    "ORACLE_MONITORING_USER": "u", "ORACLE_MONITORING_PASSWORD": "p",
    "VALIDATOR_INGEST_PATH": "x",
}


def _load(tmp_path, body, monkeypatch, clear=()):
    for k in clear:
        monkeypatch.delenv(k, raising=False)
    f = tmp_path / ".env"
    f.write_text(body)
    load_dotenv(f)


def test_strips_inline_comment(tmp_path, monkeypatch):
    _load(tmp_path, "VALIDATOR_TOLERANCE_GAUGE=0.02      # 2% tolerance\n",
          monkeypatch, clear=["VALIDATOR_TOLERANCE_GAUGE"])
    assert os.environ["VALIDATOR_TOLERANCE_GAUGE"] == "0.02"


def test_keeps_hash_without_leading_space(tmp_path, monkeypatch):
    # password ending in '##' and a 'C##' common user must survive verbatim
    _load(tmp_path,
          "ORACLE_MONITORING_PASSWORD=YourNewPassword123##\n"
          "ORACLE_MONITORING_USER=C##DB_MONITOR\n",
          monkeypatch, clear=["ORACLE_MONITORING_PASSWORD", "ORACLE_MONITORING_USER"])
    assert os.environ["ORACLE_MONITORING_PASSWORD"] == "YourNewPassword123##"
    assert os.environ["ORACLE_MONITORING_USER"] == "C##DB_MONITOR"


def test_quoted_value_taken_verbatim(tmp_path, monkeypatch):
    _load(tmp_path, 'NEW_RELIC_API_KEY="abc # not-a-comment"\n',
          monkeypatch, clear=["NEW_RELIC_API_KEY"])
    assert os.environ["NEW_RELIC_API_KEY"] == "abc # not-a-comment"


def test_existing_env_not_overwritten(tmp_path, monkeypatch):
    monkeypatch.setenv("ORACLE_HOST", "real-host")
    _load(tmp_path, "ORACLE_HOST=from-file\n", monkeypatch)
    assert os.environ["ORACLE_HOST"] == "real-host"


def test_full_line_comment_and_blank_ignored(tmp_path, monkeypatch):
    _load(tmp_path, "# a comment\n\nORACLE_PORT=1521\n",
          monkeypatch, clear=["ORACLE_PORT"])
    assert os.environ["ORACLE_PORT"] == "1521"


def test_thick_mode_off_by_default():
    cfg = load_config(dict(_BASE_ENV))
    assert cfg.oracle_thick is False
    assert cfg.oracle_client_lib_dir == ""


def test_thick_mode_enabled_by_flag():
    cfg = load_config({**_BASE_ENV, "VALIDATOR_ORACLE_THICK": "1"})
    assert cfg.oracle_thick is True


def test_lib_dir_implies_thick_mode():
    cfg = load_config({**_BASE_ENV, "ORACLE_CLIENT_LIB_DIR": "/opt/oracle/ic"})
    assert cfg.oracle_thick is True
    assert cfg.oracle_client_lib_dir == "/opt/oracle/ic"
