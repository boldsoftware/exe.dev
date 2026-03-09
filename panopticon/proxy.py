"""Proxy object system for bridging external data sources into the RLM sandbox.

ProxyObject is the base class for data sources (Discord, GitHub, Slack, etc.).
ProxyRegistry maps proxy IDs to live objects and resolves attribute access
and method call requests from the sandbox via the UDS mux server.
"""

import threading
from typing import Any, Callable


class ProxyObject:
    """Base class for objects that can be passed into the RLM sandbox as proxy references.

    Subclasses represent external data sources. Inside the sandbox, they appear
    as ProxyRef instances whose attribute access triggers tool calls back to the host.

    Subclasses should set the proxy metadata in __init__ via super().__init__(...),
    then define regular Python attributes for the data they expose. Attribute access
    from the sandbox is resolved by getattr() on the live object.

    Methods can be exposed via the `methods` parameter — a dict mapping method names
    to bound callables. Methods appear in dir() and __attr_docs__ alongside attributes.
    In the sandbox, they become callable ProxyMethod wrappers.
    """

    _proxy_id: str
    _proxy_type_name: str
    _proxy_doc: str
    _proxy_dir: list[str]
    _proxy_attr_docs: dict[str, str]
    _proxy_methods: dict[str, Callable]

    def __init__(
        self,
        proxy_id: str,
        type_name: str,
        doc: str = "",
        dir_attrs: list[str] | None = None,
        attr_docs: dict[str, str] | None = None,
        methods: dict[str, Callable] | None = None,
    ):
        self._proxy_id = proxy_id
        self._proxy_type_name = type_name
        self._proxy_doc = doc
        self._proxy_dir = dir_attrs or []
        self._proxy_attr_docs = attr_docs or {}
        self._proxy_methods = methods or {}


class ProxyRegistry:
    """Maps proxy IDs to live Python objects and resolves attribute access.

    Thread-safe. Used by the PythonInterpreter to handle _proxy_getattr
    tool calls from the sandbox.
    """

    def __init__(self):
        self._lock = threading.RLock()
        self._objects: dict[str, ProxyObject] = {}

    def register(self, obj: ProxyObject) -> str:
        """Register a proxy object and return its ID."""
        with self._lock:
            self._objects[obj._proxy_id] = obj
            return obj._proxy_id

    def _classify_value(self, value: Any) -> dict:
        """Classify a value as proxy or concrete, recursively handling containers."""
        if isinstance(value, ProxyObject):
            self.register(value)
            return {
                "type": "proxy",
                "proxy_id": value._proxy_id,
                "type_name": value._proxy_type_name,
                "doc": value._proxy_doc,
                "dir": value._proxy_dir,
                "attr_docs": value._proxy_attr_docs,
            }
        elif isinstance(value, list):
            items = [self._classify_value(item) for item in value]
            if any(item["type"] != "concrete" for item in items):
                return {"type": "proxy_list", "items": items}
            return {"type": "concrete", "value": [item["value"] for item in items]}
        elif isinstance(value, dict):
            result = {}
            has_proxy = False
            for k, v in value.items():
                classified = self._classify_value(v)
                result[k] = classified
                if classified["type"] != "concrete":
                    has_proxy = True
            if has_proxy:
                return {"type": "proxy_dict", "items": result}
            return {"type": "concrete", "value": {k: v["value"] for k, v in result.items()}}
        elif isinstance(value, (str, int, float, bool, type(None))):
            return {"type": "concrete", "value": value}
        else:
            raise TypeError(
                f"Value of type {type(value).__name__} is not serializable"
            )

    def resolve_getattr(self, proxy_id: str, attr: str) -> dict:
        """Resolve attribute access on a proxy object.

        Returns a discriminated union:
        - {"type": "proxy", ...} for proxy results
        - {"type": "concrete", "value": ...} for data results
        - {"type": "proxy_list", "items": [...]} for lists containing proxies
        - {"type": "proxy_dict", "items": {...}} for dicts containing proxies
        """
        with self._lock:
            obj = self._objects.get(proxy_id)
            if obj is None:
                raise KeyError(f"Unknown proxy ID: {proxy_id}")

            # Special attributes
            if attr == "__doc__":
                return {"type": "concrete", "value": obj._proxy_doc}
            if attr == "__dir__":
                return {"type": "concrete", "value": obj._proxy_dir}
            if attr == "__attr_docs__":
                return {"type": "concrete", "value": obj._proxy_attr_docs}
            if attr == "__iter__":
                # If the object is iterable, return its items classified
                try:
                    items = list(iter(obj))
                except TypeError:
                    raise AttributeError(f"{obj._proxy_type_name} is not iterable")
                return self._classify_value(items)

            if attr not in obj._proxy_dir:
                raise AttributeError(
                    f"Attribute '{attr}' is not exposed on {obj._proxy_type_name}. "
                    f"Available: {', '.join(obj._proxy_dir)}"
                )

            # Methods return a descriptor; the sandbox wraps it as a callable
            if attr in obj._proxy_methods:
                return {"type": "method", "name": attr, "proxy_id": proxy_id}

            value = getattr(obj, attr)
            return self._classify_value(value)

    def resolve_call(self, proxy_id: str, method: str, args: list, kwargs: dict) -> dict:
        """Resolve a method call on a proxy object.

        Returns the same discriminated union as resolve_getattr.
        """
        with self._lock:
            obj = self._objects.get(proxy_id)
            if obj is None:
                raise KeyError(f"Unknown proxy ID: {proxy_id}")

            if method not in obj._proxy_methods:
                raise AttributeError(
                    f"Method '{method}' is not exposed on {obj._proxy_type_name}"
                )

            # All args/kwargs must be JSON primitives
            for v in list(args) + list(kwargs.values()):
                if not isinstance(v, (str, int, float, bool, type(None))):
                    raise TypeError(
                        f"Method argument of type {type(v).__name__} is not a JSON primitive"
                    )

            result = obj._proxy_methods[method](*args, **kwargs)
            return self._classify_value(result)
