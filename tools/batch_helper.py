#!/usr/bin/env python3
import sys
import subprocess
import json
from pathlib import Path

def run_bulk_save(db_path, json_file):
    """Safely saves a JSON file (UTF-8) to the SQLite DB via bulk-save."""
    try:
        with open(json_file, 'rb') as f:
            payload = f.read()
        
        # Call the existing translate.py bulk-save command
        result = subprocess.run(
            ['uv', 'run', 'tools/translate.py', '-d', db_path, 'bulk-save'],
            input=payload,
            capture_output=True,
            check=True
        )
        print(result.stdout.decode('utf-8'))
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)

if __name__ == "__main__":
    if len(sys.argv) != 3:
        print("Usage: python tools/batch_helper.py [DB_PATH] [JSON_FILE]")
        sys.exit(1)
    run_bulk_save(sys.argv[1], sys.argv[2])
