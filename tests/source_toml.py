"""Encode only synthetic JSON-shaped test models as canonical source TOML."""
import json
import math


def source_text(value):
    def literal(item):
        if isinstance(item, str): return json.dumps(item, ensure_ascii=False)
        if type(item) is bool: return str(item).lower()
        if type(item) in (int, float) and math.isfinite(item): return repr(item)
        if isinstance(item, list): return '[' + ', '.join(map(literal, item)) + ']'
        if isinstance(item, dict): return '{' + ', '.join(json.dumps(key, ensure_ascii=False) + ' = ' + literal(val) for key, val in item.items()) + '}'
        raise ValueError('unrepresentable source test value')
    return ''.join(json.dumps(key, ensure_ascii=False) + ' = ' + literal(item) + '\n' for key, item in value.items())
