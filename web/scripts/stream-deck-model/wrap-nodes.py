#!/usr/bin/env python3
"""Wrap each key and dial mesh node of a .glb in a plain parent that keeps its name.

Run between `weld` and `quantize` (see compress.sh). KHR_mesh_quantization moves
a mesh's scale and offset into the transform of the node that holds the mesh.
scene.ts moves the key and dial nodes, adds children to them and reads their
position, so those nodes must keep their own transform and their children. After
this step the named node is a parent with the original translation and no mesh,
and an unnamed child holds the mesh, so quantize puts its transform on the child.

usage: python3 -I wrap-nodes.py in.glb out.glb
"""
import json
import re
import struct
import sys

WRAP = re.compile(r"^(key|dial)\d+$")


def main(src, dst):
    data = open(src, "rb").read()
    magic, version, _total = struct.unpack("<4sII", data[:12])
    assert magic == b"glTF" and version == 2
    jlen, jtype = struct.unpack("<I4s", data[12:20])
    assert jtype == b"JSON"
    doc = json.loads(data[20:20 + jlen])
    rest = data[20 + jlen:]  # the BIN chunk, untouched
    nodes = doc["nodes"]
    for node in list(nodes):
        if WRAP.match(node.get("name", "")) and "mesh" in node:
            inner = {"mesh": node.pop("mesh")}
            nodes.append(inner)
            node["children"] = [len(nodes) - 1]
    body = json.dumps(doc, separators=(",", ":")).encode()
    body += b" " * (-len(body) % 4)
    out = struct.pack("<I4s", len(body), b"JSON") + body + rest
    open(dst, "wb").write(struct.pack("<4sII", b"glTF", 2, 12 + len(out)) + out)


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
