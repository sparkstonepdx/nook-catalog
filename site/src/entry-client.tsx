import { hydrate } from "@solidjs/web";
import "@sparkstone/css";
import "./tokens.css";
import "./site.css";
import App from "./App";

hydrate(() => <App path={location.pathname} />, document.getElementById("root")!);
