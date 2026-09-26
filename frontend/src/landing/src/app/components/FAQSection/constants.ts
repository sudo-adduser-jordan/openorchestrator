export interface FAQItem {
  question: string;
  answer: string;
}

export const FAQ_ITEMS: FAQItem[] = [
  {
    question: "I already use an IDE like Cursor, is this for me?",
    answer:
      "Open Agents is designed to work with your existing tool. We natively support deep-linking to IDEs like Cursor so you can open your workspaces and files in your IDE. Open Agents sits above individual tools, use whatever agent you like, Open Agents keeps the workflow the same.",
  },
  {
    question: "Which AI coding agents are supported?",
    answer:
      "Open Agents works with any CLI-based coding agent including OpenAI OpenCode, OpenCode, Cursor, Aider, Goose, and 18 more. If it runs in a terminal, it runs in Open Agents. 23 harnesses total, with per-project agent choice.",
  },
  {
    question: "How does the parallel agent system work?",
    answer:
      "Each agent runs in its own isolated Git worktree, which means they can work on different branches or features simultaneously without conflicts. Open Agents's manager spawns workers, routes CI failures and review feedback to the right session, and lets you monitor the entire fleet from one board.",
  },
  {
    question: "Is Open Agents free to use?",
    answer:
      "Yes. Open Agents is free and open source under Apache 2.0. It runs as a local daemon on your machine, your code never leaves localhost. No account, no cloud, no credit card required.",
  },
  {
    question: "Can I use my own API keys?",
    answer:
      "Absolutely. Open Agents doesn't proxy any API calls. You use your own API keys directly with whatever AI providers you choose. This means you have full control over costs and usage.",
  },
];
