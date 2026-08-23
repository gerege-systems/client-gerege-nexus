/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package documents

import "errors"

// ErrInvalidConfiguration is what a chain or a policy is refused with. Every
// refusal wraps it and adds the sentence that says which step and why, because
// an administrator with a ten-step chain needs to know where to look.
var ErrInvalidConfiguration = errors.New("invalid configuration")
