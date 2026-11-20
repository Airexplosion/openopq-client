"""Runnable OneBot 11 examples. Python 3, standard library only."""
import argparse
import base64
import json
import os
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


def call(action, params):
    base = os.getenv("OPENOPQ_ONEBOT_URL", "http://127.0.0.1:8087/onebot/v11")
    headers = {"Content-Type": "application/json"}
    token = os.getenv("OPENOPQ_HTTP_TOKEN")
    if token:
        headers["Authorization"] = "Bearer " + token
    request = Request(base.rstrip("/") + "/" + action, json.dumps(params).encode(), headers)
    with urlopen(request, timeout=60) as response:
        result = json.load(response)
    if result.get("retcode") != 0 or result.get("status") != "ok":
        raise ValueError(result.get("wording") or str(result.get("retcode")))
    return result["data"]


def main():
    cli = argparse.ArgumentParser(description=__doc__)
    commands = cli.add_subparsers(dest="command", required=True)
    reply = commands.add_parser("reply", help="Send a quoted group message")
    reply.add_argument("group", type=int)
    reply.add_argument("message_id")
    reply.add_argument("text")
    commands.add_parser("forward", help="Read a received forward segment").add_argument("id")
    commands.add_parser("media", help="Locate original media in the local cache").add_argument("file")
    files = commands.add_parser("group-files", help="List files in a group root")
    files.add_argument("group", type=int)
    url = commands.add_parser("group-file-url", help="Get the download URL for a listed group file")
    url.add_argument("group", type=int)
    url.add_argument("file_id")
    url.add_argument("busid", type=int)
    voice = commands.add_parser("voice", help="Send an audio file to a group")
    voice.add_argument("group", type=int)
    voice.add_argument("audio", type=Path)
    args = cli.parse_args()
    if args.command == "reply":
        segments = [{"type": "reply", "data": {"id": args.message_id}},
                    {"type": "text", "data": {"text": args.text}}]
        return call("send_group_msg", {"group_id": args.group, "message": segments})
    if args.command == "forward":
        return call("get_forward_msg", {"id": args.id})
    if args.command == "media":
        return call("get_file", {"file": args.file})
    if args.command == "group-files":
        return call("get_group_root_files", {"group_id": args.group})
    if args.command == "group-file-url":
        return call("get_group_file_url", {"group_id": args.group, "file_id": args.file_id, "busid": args.busid})
    encoded = base64.b64encode(args.audio.read_bytes()).decode("ascii")
    return call("send_group_msg", {"group_id": args.group, "message": [
        {"type": "record", "data": {"file": "base64://" + encoded}}]})


if __name__ == "__main__":
    try:
        print(json.dumps(main(), ensure_ascii=False, indent=2))
    except (HTTPError, URLError, OSError, ValueError) as error:
        raise SystemExit(str(error)) from None
