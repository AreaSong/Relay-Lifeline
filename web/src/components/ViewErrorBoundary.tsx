import { Component, type ErrorInfo, type ReactNode } from "react";

interface Props {
  children: ReactNode;
  description: string;
  reloadLabel: string;
  title: string;
}

interface State {
  failed: boolean;
}

export class ViewErrorBoundary extends Component<Props, State> {
  state: State = { failed: false };

  static getDerivedStateFromError(): State {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Relay-Lifeline view rendering failed", error, info);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return <section className="view-error" role="alert">
      <h2>{this.props.title}</h2>
      <p>{this.props.description}</p>
      <button className="button primary" onClick={() => window.location.reload()}>{this.props.reloadLabel}</button>
    </section>;
  }
}
