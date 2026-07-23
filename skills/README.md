# OpenCode内置CTF Skills

本目录保存容器内OpenCode可发现的CTF解题Skill。当前内置6个主题型，每个目录使用原生`SKILL.md`入口:

```text
crypto/SKILL.md
web/SKILL.md
pwn/SKILL.md
reverse/SKILL.md
forensics/SKILL.md
misc/SKILL.md
```

各题型的`references/`目录保存详细资料。Go服务将整个目录只读挂载到容器的`/workspace/.opencode/skills`，OpenCode可按需发现引用；桥接脚本也读取对应`SKILL.md`作为首轮上下文，不再按字符数截断。

两份历史大文档`reference/crypto/modern-ciphers-3.md`和`reference/web/server-side-deser.md`保留兼容路径，题型Skill使用显式相对链接引用；不要复制出第二份内容。

推荐加载规则：

```text
题型为crypto     -> crypto/SKILL.md
题型为web        -> web/SKILL.md
题型为pwn        -> pwn/SKILL.md
题型为reverse    -> reverse/SKILL.md
题型为forensics  -> forensics/SKILL.md
其他或混合题型    -> misc/SKILL.md
```

新增资料时必须记录来源和许可证，修复相对链接，并运行`./scripts/check-markdown-links.ps1`。
