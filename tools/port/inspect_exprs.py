import sys, json
sys.path.insert(0, sys.argv[1])
from sqlglot import exp
classes = exp.EXPR_CLASSES
print("num classes", len(classes))
traits = set()
for k, cls in classes.items():
    for b in cls.__mro__:
        if b.__name__ not in classes or classes[b.__name__.lower()] is not b:
            traits.add(b.__name__)
print("non-registered bases:", sorted(traits))
# check key uniqueness / name mismatches
for k, cls in list(classes.items())[:5]:
    print(k, cls.__name__, [b.__name__ for b in cls.__mro__], cls.arg_types, cls.is_var_len_args)
print("funcs:", len(exp.ALL_FUNCTIONS))
# sql names with multiple names
multi = [(f.__name__, f.sql_names()) for f in exp.ALL_FUNCTIONS if len(f.sql_names()) > 1]
print("multi-name funcs:", len(multi), multi[:10])
# check classes with extra class attributes that differ from defaults
attrs = set()
for k, cls in classes.items():
    for a, v in cls.__dict__.items():
        if a.isupper() or (a.startswith('_') and not a.startswith('__')) or a.startswith('is_'):
            attrs.add(a)
print("class attrs:", sorted(attrs))
