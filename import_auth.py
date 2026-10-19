#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
AIStudio2API - 浏览器账号认证信息一键导入工具 (纯 Python3 实现)

功能:
- 默认自动扫描 auth/ 目录下的所有 JSON 认证文件，或手动指定文件/目录导入
- 自动识别并提取 Google 邮箱 (accountName / email / aistudio2api 扩展字段)
- 校验关键签名凭据 (SAPISID, __Secure-1PAPISID, __Secure-3PAPISID)
- 遇到已有账号自动更新 Cookie，保留原有代理/时区配置
- 纯 Python 3 标准库实现，零外部依赖
"""

import argparse
import json
import os
import re
import sys
from pathlib import Path
from typing import Dict, List, Optional, Tuple

# 终端色彩支持
USE_COLOR = sys.stdout.isatty() and os.name != "nt" or "WT_SESSION" in os.environ


def color_text(text: str, color_code: str) -> str:
    if not USE_COLOR:
        return text
    return f"\033[{color_code}m{text}\033[0m"


def info(msg: str) -> None:
    print(f"{color_text('[INFO]', '34')} {msg}")


def success(msg: str) -> None:
    print(f"{color_text('[SUCCESS]', '32')} {msg}")


def warn(msg: str) -> None:
    print(f"{color_text('[WARN]', '33')} {msg}")


def error(msg: str) -> None:
    print(f"{color_text('[ERROR]', '31')} {msg}")


EMAIL_REGEX = re.compile(r"^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$")
REQUIRED_SIGNATURE_COOKIES = ["SAPISID", "__Secure-1PAPISID", "__Secure-3PAPISID"]
IGNORED_JSON_NAMES = {"account.json", "storage-state.json", "runtime-state.json"}


def parse_env_auth_dir(env_path: Path) -> Optional[Path]:
    """尝试从 .env 读取 AISTUDIO_AUTH_STATES 配置"""
    if not env_path.is_file():
        return None
    try:
        with open(env_path, "r", encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if line.startswith("#") or not line:
                    continue
                if line.startswith("AISTUDIO_AUTH_STATES="):
                    val = line.split("=", 1)[1].strip().strip('"').strip("'")
                    if val and "," not in val:
                        return (env_path.parent / val).resolve()
    except Exception:
        pass
    return None


def extract_email(data: dict) -> Optional[str]:
    """从凭据字典中提取 Google 邮箱"""
    # 1. 顶层 accountName
    if isinstance(data.get("accountName"), str) and data["accountName"].strip():
        return data["accountName"].strip().lower()

    # 2. 顶层 email
    if isinstance(data.get("email"), str) and data["email"].strip():
        return data["email"].strip().lower()

    # 3. aistudio2api.source.email
    ext = data.get("aistudio2api")
    if isinstance(ext, dict):
        source = ext.get("source")
        if isinstance(source, dict) and source.get("email"):
            return str(source["email"]).strip().lower()

    return None


def validate_cookies(cookies: list) -> Tuple[bool, List[str], int]:
    """校验 Cookie 列表中是否包含核心签名项"""
    if not isinstance(cookies, list):
        return False, REQUIRED_SIGNATURE_COOKIES, 0

    present_names = {c.get("name") for c in cookies if isinstance(c, dict)}
    missing = [c for c in REQUIRED_SIGNATURE_COOKIES if c not in present_names]
    return len(missing) == 0, missing, len(cookies)


def prompt_for_email(file_name: str) -> str:
    """在缺失邮箱信息时，通过终端交互请求用户输入"""
    warn(f"文件 {file_name} 中未能自动检测到 Google 邮箱。")
    while True:
        try:
            val = input("请输入该凭据对应的 Google 邮箱 (例如 user@gmail.com): ").strip().lower()
        except (KeyboardInterrupt, EOFError):
            print("\n已取消操作。")
            sys.exit(1)
        if EMAIL_REGEX.match(val):
            return val
        error("邮箱格式无效，请重新输入！")


def import_account_file(file_path: Path, auth_root: Path) -> bool:
    """导入单个认证 JSON 文件"""
    print(f"\n{color_text('正在处理文件:', '1')} {file_path}")

    # 读取并解析 JSON
    try:
        with open(file_path, "r", encoding="utf-8") as f:
            data = json.load(f)
    except Exception as e:
        error(f"无法读取或解析 JSON 文件: {e}")
        return False

    if not isinstance(data, dict):
        error("JSON 内容必须为字典对象")
        return False

    # 检查并校验 Cookie
    cookies = data.get("cookies", [])
    valid_cookies, missing, total_count = validate_cookies(cookies)
    if total_count == 0:
        error("文件中缺少有效 cookies 列表")
        return False

    if not valid_cookies:
        warn(f"缺少核心签名 Cookie: {', '.join(missing)}，该账号导入后可能无法通过 AI Studio 认证")
    else:
        print(f"  {color_text('•', '32')} 成功校验核心 SAPISID 凭据 (共包含 {total_count} 个 Cookie)")

    # 确定目标邮箱
    email = extract_email(data)
    if not email or not EMAIL_REGEX.match(email):
        email = prompt_for_email(file_path.name)

    info(f"目标账号: {color_text(email, '36')}")

    target_dir = auth_root / email
    target_storage = target_dir / "storage-state.json"
    target_config = target_dir / "account.json"

    target_dir.mkdir(parents=True, exist_ok=True)

    # 写入 storage-state.json
    try:
        with open(target_storage, "w", encoding="utf-8") as f:
            json.dump(data, f, ensure_ascii=False, indent=2)
            f.write("\n")
        try:
            os.chmod(target_storage, 0o600)
        except Exception:
            pass
    except Exception as e:
        error(f"写入 {target_storage} 失败: {e}")
        return False

    # 检查并写入 account.json 配置
    if not target_config.exists():
        default_config = {
            "label": email,
            "enabled": True,
            "proxy": "",
            "locale": "zh-CN",
            "timezone": "Asia/Shanghai",
        }
        try:
            with open(target_config, "w", encoding="utf-8") as f:
                json.dump(default_config, f, ensure_ascii=False, indent=2)
                f.write("\n")
            try:
                os.chmod(target_config, 0o600)
            except Exception:
                pass
            success(f"【新账号创建成功】{email}")
            info(f"配置文件已生成: {target_dir}")
        except Exception as e:
            error(f"生成 account.json 失败: {e}")
            return False
    else:
        success(f"【账号 Cookie 更新成功】{email}")
        info(f"保留了已有代理与时区配置，仅更新认证凭据: {target_storage}")

    return True


def collect_files(targets: List[str], default_scan_dir: Path) -> List[Path]:
    """根据传入参数或默认扫描目录搜集所有待导入文件"""
    files: List[Path] = []
    if targets:
        for t in targets:
            p = Path(t)
            if p.is_file():
                files.append(p)
            elif p.is_dir():
                files.extend(sorted(f for f in p.glob("*.json") if f.name not in IGNORED_JSON_NAMES))
            else:
                warn(f"参数不存在或非有效文件/目录，已跳过: {t}")
    else:
        info(f"未指定参数，正在扫描 {default_scan_dir} 目录下的 JSON 认证文件...")
        if default_scan_dir.is_dir():
            files = sorted(f for f in default_scan_dir.glob("*.json") if f.name not in IGNORED_JSON_NAMES)

    # 去重
    unique_files: List[Path] = []
    seen = set()
    for f in files:
        resolved = f.resolve()
        if resolved not in seen:
            seen.add(resolved)
            unique_files.append(f)
    return unique_files


def main():
    script_dir = Path(__file__).resolve().parent

    parser = argparse.ArgumentParser(
        description="AIStudio2API 浏览器账号认证信息一键导入工具 (纯 Python3 实现)",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
示例:
  python3 import_auth.py                     # 自动扫描 auth/ 目录下的所有待导入 JSON 文件
  python3 import_auth.py runtime/auth-5.json # 导入指定文件
  python3 import_auth.py /path/to/files/     # 导入指定目录下的所有 JSON 文件
        """,
    )
    parser.add_argument(
        "paths",
        nargs="*",
        help="待导入的 JSON 文件或包含 JSON 文件的目录路径 (默认扫描 auth/*.json)",
    )
    parser.add_argument(
        "--auth-dir",
        type=str,
        default=None,
        help="目标账号保存目录 (默认自动读取 .env 的 AISTUDIO_AUTH_STATES，无则使用 auth)",
    )
    parser.add_argument(
        "--scan-dir",
        type=str,
        default=None,
        help="未传参时的默认扫描目录 (默认与 auth 目录相同)",
    )

    args = parser.parse_args()

    # 确定 auth 存储目录 (优先读 .env)
    if args.auth_dir:
        auth_root = Path(args.auth_dir).resolve()
    else:
        env_auth = parse_env_auth_dir(script_dir / ".env")
        auth_root = env_auth if env_auth else (script_dir / "auth")

    # 默认搜索路径设为 auth 目录
    if args.scan_dir:
        scan_dir = Path(args.scan_dir).resolve()
    else:
        scan_dir = auth_root

    auth_root.mkdir(parents=True, exist_ok=True)

    files_to_process = collect_files(args.paths, scan_dir)

    if not files_to_process:
        warn("未找到任何待导入的认证文件！")
        print("\n使用提示:")
        print(f"  1. 自动模式: 将浏览器导出的文件放入 {scan_dir} 目录 (如 auth/auth-1.json)，然后运行:")
        print("     python3 import_auth.py")
        print("  2. 手动模式: 传入文件路径，支持一次传入多个文件:")
        print("     python3 import_auth.py runtime/auth-5.json")
        print("     python3 import_auth.py /path/to/account.json")
        sys.exit(0)

    print("\n" + "=" * 56)
    print(f"{color_text('AIStudio2API 账号凭据导入器', '1')}")
    print(f"待处理文件数量: {color_text(str(len(files_to_process)), '32')} 个")
    print(f"搜索扫描目录:   {scan_dir}")
    print(f"账号保存目录:   {auth_root}")
    print("=" * 56)

    success_count = 0
    fail_count = 0

    for file_path in files_to_process:
        if import_account_file(file_path, auth_root):
            success_count += 1
        else:
            fail_count += 1

    print("\n" + "=" * 56)
    print(f"{color_text('导入结果汇总:', '1')}")
    print(f"  成功: {color_text(str(success_count), '32')} 个")
    if fail_count > 0:
        print(f"  失败: {color_text(str(fail_count), '31')} 个")
    print("=" * 56)

    info(f"当前已保存账号查看: ls -la {auth_root}")
    info("启动服务命令: ./aistudio2api (管理端: http://127.0.0.1:2048)")


if __name__ == "__main__":
    main()
